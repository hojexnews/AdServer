// Package kmsenvelope implementa cifra de envelope simetrico para colunas PII
// do cofre db/compliance (K6 hardening — ADR-0004 §F).
//
// # Principio de fronteira
//
// A cifra incide EXCLUSIVAMENTE sobre colunas TEXT de nome (PII) no cofre
// db/compliance. NUNCA toca amount_minor_units nem qualquer valor monetario
// (invariante TX-2).
//
// # Interface plugavel
//
// KMSEnvelope e a unica interface que o resto do codigo usa. Ha duas
// implementacoes:
//
//   - LocalEnvelope: em dev/CI — chave derivada de PII_ENVELOPE_KEY (env).
//     A chave NUNCA e hardcoded — lida do ambiente ou recusada.
//
//   - ExternalEnvelope: fronteira para KMS externo (ex.: AWS KMS, OpenBao Transit).
//     Em producao, o KMS real e injetado pelo operator via openbao-agent.
//     Basta implementar a interface e injetar no NewFromEnv.
//
// # Algoritmo
//
// AES-256-GCM com nonce aleatorio de 12 bytes (recomendado pelo NIST SP 800-38D).
// Output: base64url(nonce || ciphertext || tag) — cabe em TEXT sem escapamento.
//
// A chave local e derivada do material em PII_ENVELOPE_KEY via SHA-256
// para garantir exatamente 32 bytes independente do comprimento da env.
//
// # Invariantes
//
//   - TX-3: PII permanece no cofre compliance, referenciada por tenant_id pseudonimo.
//   - DA-11: plaintext NUNCA aparece em logs.
//   - TX-2: a cifra NAO e chamada sobre valores monetarios.
//   - Chave NUNCA hardcoded em codigo ou imagem — lida do ambiente (dev/CI)
//     ou do KMS (producao).
package kmsenvelope

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// ---------------------------------------------------------------------------
// Versionamento do formato selado (MEDIUM-1)
//
// O formato selado e:
//   sealedVersion + "$" + base64url(nonce || ciphertext || tag)
//
// sealedVersion = "v1" hoje. Versoes desconhecidas sao rejeitadas por Open
// (fail-closed), permitindo rotacao segura de chave/algoritmo no futuro:
// a nova versao usa um prefixo diferente ("v2$...") e o Open continua
// entendendo "v1$..." enquanto o historico existir.
//
// Por que prefixo textual e nao byte?
//   - Compatibilidade com colunas TEXT/VARCHAR sem escapamento adicional.
//   - Legivel por operadores que inspecionam dados cifrados (auditoria).
//   - O delimitador "$" nao ocorre em base64url — separacao sem ambiguidade.
//
// O key_id e implicitamente "v1" (chave derivada de PII_ENVELOPE_KEY).
// Quando o KMS externo for usado (ExternalEnvelope), o key_id real (ex.:
// ARN do KMS) pode ser embutido como "v2:<arn>$..." — o Open deve ser
// atualizado para essa versao. Por enquanto, "v1" e a unica versao valida.
// ---------------------------------------------------------------------------

const (
	// sealedVersion e o prefixo de versao atual do formato selado.
	sealedVersion = "v1"

	// sealedDelimiter separa o prefixo de versao do payload base64.
	sealedDelimiter = "$"

	// sealedPrefix e o prefixo completo adicionado antes do base64 payload.
	sealedPrefix = sealedVersion + sealedDelimiter
)

// ---------------------------------------------------------------------------
// Interface principal
// ---------------------------------------------------------------------------

// KMSEnvelope e a interface de cifra/decifra de envelope simetrico.
//
// Seal cifra plaintext PII antes do INSERT no cofre compliance.
// Open decifra ciphertext lido do cofre compliance.
//
// Implementacoes devem ser seguras para uso concorrente.
// Erros de Seal e Open nunca devem incluir o plaintext na mensagem.
type KMSEnvelope interface {
	// Seal cifra o plaintext e retorna o ciphertext codificado (base64url).
	// plaintext nao deve ser logado pelo caller.
	Seal(plaintext string) (string, error)

	// Open decifra o ciphertext codificado retornado por Seal.
	// O plaintext retornado nao deve ser logado pelo caller.
	Open(ciphertext string) (string, error)
}

// ---------------------------------------------------------------------------
// LocalEnvelope — implementacao dev/CI com chave local
// ---------------------------------------------------------------------------

// LocalEnvelope implementa KMSEnvelope com AES-256-GCM e chave local.
// Usado em dev/CI; em producao, substituir por ExternalEnvelope (KMS real).
//
// A chave e derivada via SHA-256 do material lido de PII_ENVELOPE_KEY,
// produzindo sempre 32 bytes independente do comprimento da env.
//
// SEGURANCA: nunca incluir a chave em logs, stacks de erro ou imagens.
type LocalEnvelope struct {
	key [32]byte // AES-256: 32 bytes fixos
}

// newLocalFromKey cria um LocalEnvelope a partir de material de chave raw.
// Deriva 32 bytes via SHA-256 com prefixo de dominio para separacao de contexto.
func newLocalFromKey(keyMaterial []byte) (*LocalEnvelope, error) {
	if len(keyMaterial) == 0 {
		return nil, errors.New("kmsenvelope: material de chave vazio")
	}
	// Derivacao simples: SHA-256(domainSep || keyMaterial)
	// Domain separation evita reuso da mesma chave em contextos distintos.
	h := sha256.New()
	h.Write([]byte("kmsenvelope-pii-v1:")) // domain separator
	h.Write(keyMaterial)
	var key [32]byte
	copy(key[:], h.Sum(nil))
	return &LocalEnvelope{key: key}, nil
}

// NewFromEnv cria um KMSEnvelope lido da variavel de ambiente PII_ENVELOPE_KEY.
//
// Em dev/CI:
//   - Se PII_ENVELOPE_KEY estiver definida, usa o valor como material de chave.
//   - Se nao estiver definida, retorna erro (fail-closed: sem silencio em CI).
//
// Em producao, a variavel deve ser injetada pelo OpenBao (nunca em git/imagem).
//
// O caller (travelrule.go / sumsub.go) e responsavel por chamar esta funcao
// no boot e injetar o envelope por dependency injection — nunca no hot path.
func NewFromEnv() (KMSEnvelope, error) {
	raw := os.Getenv("PII_ENVELOPE_KEY")
	if raw == "" {
		return nil, errors.New(
			"kmsenvelope: PII_ENVELOPE_KEY ausente — " +
				"configure a variavel de ambiente para cifra PII (dev/CI) " +
				"ou injete o KMS externo (producao)",
		)
	}
	return newLocalFromKey([]byte(raw))
}

// NewForTest cria um KMSEnvelope deterministico para testes unitarios.
//
// A chave de teste e fixa e conhecida — NUNCA usar em producao.
// O nome explicito impede uso acidental.
func NewForTest() KMSEnvelope {
	// Chave de teste documentada — sem segredo aqui.
	e, err := newLocalFromKey([]byte("kmsenvelope-test-key-do-not-use-in-production"))
	if err != nil {
		// Impossivel com chave nao-vazia; panic e aceitavel em init de testes.
		panic("kmsenvelope.NewForTest: " + err.Error())
	}
	return e
}

// Seal cifra o plaintext com AES-256-GCM e retorna v1$base64url(nonce||ct||tag).
//
// Nonce aleatorio de 12 bytes (recomendado NIST SP 800-38D para GCM).
// Output: prefixo de versao "v1$" + base64url sem padding — cabe em TEXT Postgres.
// O prefixo permite rotacao futura de chave/algoritmo sem migrar historico.
//
// Se plaintext for vazio, retorna string vazia sem cifrar (NULL-safe).
func (e *LocalEnvelope) Seal(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}

	block, err := aes.NewCipher(e.key[:])
	if err != nil {
		return "", fmt.Errorf("kmsenvelope: AES: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("kmsenvelope: GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize()) // 12 bytes
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("kmsenvelope: nonce: %w", err)
	}

	// Encrypt: nonce || ciphertext+tag (GCM appends tag)
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	// Prefixo "v1$" identifica a versao do formato: permite rotacao futura
	// de chave ou algoritmo sem quebrar a decifra do historico existente.
	return sealedPrefix + base64.RawURLEncoding.EncodeToString(sealed), nil
}

// Open decifra o ciphertext produzido por Seal.
//
// Valida o prefixo de versao (fail-closed: versao desconhecida retorna erro).
// Se ciphertext for vazio, retorna string vazia (NULL-safe).
// Retorna erro se o prefixo for invalido, o base64 estiver corrompido
// ou a autenticacao GCM falhar. O erro nunca inclui o plaintext.
func (e *LocalEnvelope) Open(ciphertext string) (string, error) {
	if ciphertext == "" {
		return "", nil
	}

	// Valida e remove o prefixo de versao (fail-closed: versao desconhecida
	// e rejeitada imediatamente — nunca tentar decifrar formato desconhecido).
	if !strings.HasPrefix(ciphertext, sealedPrefix) {
		return "", fmt.Errorf("kmsenvelope: versao do ciphertext desconhecida ou ausente (esperado prefixo %q)", sealedPrefix)
	}
	b64payload := ciphertext[len(sealedPrefix):]

	raw, err := base64.RawURLEncoding.DecodeString(b64payload)
	if err != nil {
		return "", fmt.Errorf("kmsenvelope: base64: %w", err)
	}

	block, err := aes.NewCipher(e.key[:])
	if err != nil {
		return "", fmt.Errorf("kmsenvelope: AES: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("kmsenvelope: GCM: %w", err)
	}

	ns := gcm.NonceSize()
	if len(raw) < ns+gcm.Overhead() {
		return "", errors.New("kmsenvelope: ciphertext muito curto")
	}

	nonce, ct := raw[:ns], raw[ns:]
	plainBytes, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		// Mensagem padrao do GCM nao contem PII.
		return "", fmt.Errorf("kmsenvelope: autenticacao falhou: %w", err)
	}
	return string(plainBytes), nil
}

// ---------------------------------------------------------------------------
// ExternalEnvelope — fronteira para KMS externo (producao)
// ---------------------------------------------------------------------------

// ExternalEnvelopeFunc e o tipo das funcoes de cifra/decifra do KMS externo.
// Quando o KMS real (AWS KMS, OpenBao Transit, etc.) estiver disponivel,
// implementar estas funcoes e injetar via NewExternalEnvelope.
type ExternalEnvelopeFunc func(data string) (string, error)

// ExternalEnvelope delega Seal/Open para funcoes externas (KMS real).
//
// Usado em producao quando o operator injeta as funcoes via OpenBao/KMS.
// Em dev/CI, usar LocalEnvelope (NewFromEnv).
type ExternalEnvelope struct {
	sealFn ExternalEnvelopeFunc
	openFn ExternalEnvelopeFunc
}

// NewExternalEnvelope cria um ExternalEnvelope com as funcoes do KMS externo.
// sealFn e openFn DEVEM ser nao-nil.
func NewExternalEnvelope(sealFn, openFn ExternalEnvelopeFunc) (*ExternalEnvelope, error) {
	if sealFn == nil || openFn == nil {
		return nil, errors.New("kmsenvelope: sealFn e openFn sao obrigatorios")
	}
	return &ExternalEnvelope{sealFn: sealFn, openFn: openFn}, nil
}

// Seal delega para o KMS externo.
func (e *ExternalEnvelope) Seal(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	return e.sealFn(plaintext)
}

// Open delega para o KMS externo.
func (e *ExternalEnvelope) Open(ciphertext string) (string, error) {
	if ciphertext == "" {
		return "", nil
	}
	return e.openFn(ciphertext)
}
