// Package kmsenvelope — kmsenvelope_test.go
//
// Testes unitarios do KMSEnvelope (cifra de envelope AES-256-GCM).
//
// Casos cobertos:
//   - Round-trip: Seal -> Open retorna o plaintext original.
//   - O valor persistido NAO e o plaintext (invariante de cifra).
//   - Seal produz ciphertext com prefixo de versao "v1$" (MEDIUM-1).
//   - Open rejeita ciphertext sem prefixo de versao (fail-closed).
//   - Open rejeita prefixo de versao desconhecido (fail-closed).
//   - Seal de string vazia retorna string vazia (NULL-safe).
//   - Open de string vazia retorna string vazia (NULL-safe).
//   - Open com ciphertext corrompido retorna erro.
//   - Dois Seals do mesmo plaintext produzem ciphertexts distintos (nonce aleatorio).
//   - NewFromEnv falha quando PII_ENVELOPE_KEY ausente.
//   - NewFromEnv funciona quando PII_ENVELOPE_KEY definida.
//   - ExternalEnvelope delega para funcoes externas.
//   - newLocalFromKey falha com material vazio.
//   - TX-2: cifra nao e chamada sobre valores monetarios (teste documental de tipo).
package kmsenvelope

import (
	"os"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Round-trip: Seal -> Open retorna o plaintext original
// ---------------------------------------------------------------------------

func TestRoundTrip_PlaintextRecovered(t *testing.T) {
	e := NewForTest()

	plaintexts := []string{
		"Joao da Silva Santos",
		"Maria Oliveira Costa",
		"Jane Doe",
		"Abcdefghijklmnopqrstuvwxyz 0123456789 !@#",
		// Nomes com acentos (UTF-8)
		"José Álvaro Ñoño",
	}
	for _, pt := range plaintexts {
		ct, err := e.Seal(pt)
		if err != nil {
			t.Fatalf("Seal(%q): %v", pt, err)
		}
		recovered, err := e.Open(ct)
		if err != nil {
			t.Fatalf("Open para %q: %v", pt, err)
		}
		if recovered != pt {
			t.Errorf("round-trip falhou: quero %q, got %q", pt, recovered)
		}
	}
}

// ---------------------------------------------------------------------------
// Invariante de cifra: o valor persistido NAO e o plaintext
// ---------------------------------------------------------------------------

func TestSeal_CiphertextIsNotPlaintext(t *testing.T) {
	e := NewForTest()
	plaintext := "Joao da Silva Santos"

	ct, err := e.Seal(plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	// O ciphertext nao deve conter o plaintext nem ser igual a ele.
	if ct == plaintext {
		t.Error("VIOLACAO: ciphertext e identico ao plaintext — nao ha cifra")
	}
	if strings.Contains(ct, plaintext) {
		t.Error("VIOLACAO: ciphertext contem o plaintext em claro")
	}
}

// ---------------------------------------------------------------------------
// Versionamento do formato selado (MEDIUM-1)
// ---------------------------------------------------------------------------

// TestSeal_HasVersionPrefix verifica que o ciphertext produzido por Seal
// contem o prefixo de versao "v1$" — precondition para rotacao segura de chave.
func TestSeal_HasVersionPrefix(t *testing.T) {
	e := NewForTest()
	ct, err := e.Seal("Joao da Silva Santos")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if !strings.HasPrefix(ct, "v1$") {
		t.Errorf("ciphertext sem prefixo de versao: got %q (esperado prefixo 'v1$')", ct[:min(len(ct), 10)])
	}
}

// TestOpen_RejectsUnversionedCiphertext verifica que Open rejeita um ciphertext
// sem prefixo de versao (ex.: ciphertext gerado pela versao anterior do pacote).
// Fail-closed: versao desconhecida bloqueia a decifra.
func TestOpen_RejectsUnversionedCiphertext(t *testing.T) {
	e := NewForTest()

	// Simula um ciphertext antigo (base64url puro, sem prefixo "v1$").
	// Na versao anterior, Seal retornava base64url(nonce||ct||tag) diretamente.
	oldStyleCiphertext := "AAAAAAAAAAAAAAAAAABBBBBBBBBBBBBBBBBBBBBBCCCC"
	_, err := e.Open(oldStyleCiphertext)
	if err == nil {
		t.Error("esperava erro ao abrir ciphertext sem prefixo de versao (fail-closed)")
	}
	if err != nil && !strings.Contains(err.Error(), "versao") {
		t.Errorf("mensagem de erro deveria mencionar 'versao', got: %v", err)
	}
}

// TestOpen_RejectsUnknownVersion verifica que Open rejeita prefixo de versao
// desconhecido (ex.: "v99$...") — fail-closed para versoes futuras nao suportadas.
func TestOpen_RejectsUnknownVersion(t *testing.T) {
	e := NewForTest()
	_, err := e.Open("v99$AAAAAAAAAAAAAAAAAABBBBBBBBBBBBBBBBBBBBBBCCCC")
	if err == nil {
		t.Error("esperava erro ao abrir ciphertext com versao desconhecida (fail-closed)")
	}
}

// TestSeal_Open_RoundTripWithPrefix verifica que o round-trip funciona
// corretamente com o novo formato versionado.
func TestSeal_Open_RoundTripWithPrefix(t *testing.T) {
	e := NewForTest()
	plaintext := "Nome com versao versionado"
	ct, err := e.Seal(plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	// Confirma prefixo
	if !strings.HasPrefix(ct, "v1$") {
		t.Fatalf("ciphertext sem prefixo v1$: %q", ct[:min(len(ct), 10)])
	}
	// Decifra com sucesso
	recovered, err := e.Open(ct)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if recovered != plaintext {
		t.Errorf("round-trip com prefixo: quero %q, got %q", plaintext, recovered)
	}
}

// min e um helper para limitar indices em mensagens de erro (compativel com Go 1.21+
// que tem min builtin; redundante mas seguro em versoes anteriores compilando).
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ---------------------------------------------------------------------------
// NULL-safety: strings vazias passam sem cifrar
// ---------------------------------------------------------------------------

func TestSeal_EmptyString_ReturnsEmpty(t *testing.T) {
	e := NewForTest()
	ct, err := e.Seal("")
	if err != nil {
		t.Fatalf("Seal(''): %v", err)
	}
	if ct != "" {
		t.Errorf("Seal('') deve retornar '', got %q", ct)
	}
}

func TestOpen_EmptyString_ReturnsEmpty(t *testing.T) {
	e := NewForTest()
	pt, err := e.Open("")
	if err != nil {
		t.Fatalf("Open(''): %v", err)
	}
	if pt != "" {
		t.Errorf("Open('') deve retornar '', got %q", pt)
	}
}

// ---------------------------------------------------------------------------
// Resistencia a adulteracao: Open com ciphertext corrompido retorna erro
// ---------------------------------------------------------------------------

func TestOpen_CorruptedCiphertext_ReturnsError(t *testing.T) {
	e := NewForTest()

	// Gera um ciphertext valido e corrompe o ultimo byte
	ct, err := e.Seal("Maria Oliveira Costa")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// Corrompe o ciphertext (altera o ultimo caractere base64url)
	corrupted := ct[:len(ct)-1] + "X"
	if corrupted == ct {
		corrupted = ct[:len(ct)-1] + "Y"
	}

	_, err = e.Open(corrupted)
	if err == nil {
		t.Error("esperava erro para ciphertext corrompido (autenticacao GCM deve falhar)")
	}
}

func TestOpen_InvalidBase64_ReturnsError(t *testing.T) {
	e := NewForTest()
	// Prefixo valido mas payload base64 invalido
	_, err := e.Open("v1$not-valid-base64!!!")
	if err == nil {
		t.Error("esperava erro para base64 invalido")
	}
}

func TestOpen_TooShort_ReturnsError(t *testing.T) {
	e := NewForTest()
	// Prefixo valido mas payload base64 muito curto para nonce (12) + tag (16)
	tooShort := "v1$AQIDBA" // 4 bytes decodificados — curto demais
	_, err := e.Open(tooShort)
	if err == nil {
		t.Error("esperava erro para ciphertext muito curto")
	}
}

// ---------------------------------------------------------------------------
// Probabilidade: dois Seals do mesmo plaintext devem ser distintos (nonce aleatorio)
// ---------------------------------------------------------------------------

func TestSeal_TwoCalls_ProduceDifferentCiphertexts(t *testing.T) {
	e := NewForTest()
	pt := "Joao da Silva Santos"

	ct1, err := e.Seal(pt)
	if err != nil {
		t.Fatalf("Seal 1: %v", err)
	}
	ct2, err := e.Seal(pt)
	if err != nil {
		t.Fatalf("Seal 2: %v", err)
	}
	if ct1 == ct2 {
		t.Error("dois Seals do mesmo plaintext produziram ciphertexts identicos — nonce nao e aleatorio")
	}
}

// ---------------------------------------------------------------------------
// NewFromEnv: falha sem PII_ENVELOPE_KEY, funciona com ela
// ---------------------------------------------------------------------------

func TestNewFromEnv_MissingKey_ReturnsError(t *testing.T) {
	// Remove a variavel para o teste
	prev := os.Getenv("PII_ENVELOPE_KEY")
	os.Unsetenv("PII_ENVELOPE_KEY")
	defer func() {
		if prev != "" {
			os.Setenv("PII_ENVELOPE_KEY", prev)
		}
	}()

	_, err := NewFromEnv()
	if err == nil {
		t.Fatal("esperava erro quando PII_ENVELOPE_KEY ausente")
	}
}

func TestNewFromEnv_WithKey_Works(t *testing.T) {
	os.Setenv("PII_ENVELOPE_KEY", "test-env-key-for-unit-test")
	defer os.Unsetenv("PII_ENVELOPE_KEY")

	e, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}

	ct, err := e.Seal("Joao Teste")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	pt, err := e.Open(ct)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if pt != "Joao Teste" {
		t.Errorf("round-trip: quero 'Joao Teste', got %q", pt)
	}
}

// ---------------------------------------------------------------------------
// newLocalFromKey: material vazio retorna erro
// ---------------------------------------------------------------------------

func TestNewLocalFromKey_EmptyMaterial_ReturnsError(t *testing.T) {
	_, err := newLocalFromKey([]byte{})
	if err == nil {
		t.Fatal("esperava erro para material de chave vazio")
	}
}

// ---------------------------------------------------------------------------
// ExternalEnvelope: delega para funcoes externas
// ---------------------------------------------------------------------------

func TestExternalEnvelope_DelegatesToFunctions(t *testing.T) {
	// Envelope externo simples que apenas inverte os bytes (nao e cifra real — so testa a delegacao)
	sealFn := func(pt string) (string, error) { return "sealed:" + pt, nil }
	openFn := func(ct string) (string, error) {
		if !strings.HasPrefix(ct, "sealed:") {
			return "", nil
		}
		return strings.TrimPrefix(ct, "sealed:"), nil
	}

	e, err := NewExternalEnvelope(sealFn, openFn)
	if err != nil {
		t.Fatalf("NewExternalEnvelope: %v", err)
	}

	ct, err := e.Seal("Maria Oliveira")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if ct != "sealed:Maria Oliveira" {
		t.Errorf("Seal inesperado: %q", ct)
	}

	pt, err := e.Open(ct)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if pt != "Maria Oliveira" {
		t.Errorf("Open inesperado: %q", pt)
	}
}

func TestExternalEnvelope_NilFunctions_ReturnsError(t *testing.T) {
	_, err := NewExternalEnvelope(nil, nil)
	if err == nil {
		t.Fatal("esperava erro para funcoes nil")
	}
}

func TestExternalEnvelope_Empty_Passthrough(t *testing.T) {
	called := false
	sealFn := func(pt string) (string, error) { called = true; return "x", nil }
	openFn := func(ct string) (string, error) { called = true; return "x", nil }
	e, _ := NewExternalEnvelope(sealFn, openFn)

	ct, _ := e.Seal("")
	if ct != "" {
		t.Errorf("Seal('') esperava '', got %q", ct)
	}
	if called {
		t.Error("sealFn nao deve ser chamado para plaintext vazio")
	}

	pt, _ := e.Open("")
	if pt != "" {
		t.Errorf("Open('') esperava '', got %q", pt)
	}
	if called {
		t.Error("openFn nao deve ser chamado para ciphertext vazio")
	}
}

// ---------------------------------------------------------------------------
// Invariante TX-2: cifra nao opera sobre valores monetarios (documental de tipo)
// ---------------------------------------------------------------------------

// TestNoMonetaryEncryption verifica que nenhuma funcao de cifra recebe int64.
// Este teste e documental — verifica que a interface aceita apenas string,
// tornando impossivel passar um valor monetario acidentalmente.
func TestNoMonetaryEncryption_TypeSafety(t *testing.T) {
	// Verificacao em tempo de compilacao: KMSEnvelope.Seal aceita apenas string.
	// Se alguem tentar Seal(amountMinorUnits), o compilador rejeita — TX-2 seguro.
	var _ func(string) (string, error) = NewForTest().Seal
	var _ func(string) (string, error) = NewForTest().Open
	// Nenhum campo int64 na interface — a variante monetaria (int64) e incompativel.
	t.Log("TX-2: interface KMSEnvelope aceita apenas string — valores monetarios (int64) sao incompativeis por tipo")
}
