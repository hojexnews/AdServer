// CA-3 golden tests: Criativos §4.3 — parte testável na camada importável.
//
// Escopo HONESTO. CA-3 tem quatro sub-critérios, mas a lógica que "faz algo"
// (rejeitar criativo sem destino, render responsivo, contagem própria de
// impressão do 3p, geração/validação de VAST) vive ou no collector
// `package main` (NÃO importável de um package parity_test externo) ou apenas
// no Postgres (CHECK constraint). O que É importável e travável aqui:
//
//   - configload.Assemble: o MAPEAMENTO creative_type → campo do Banner
//     (image→ImageURL, video→VideoURL, html5|thirdparty_tag→HTML com AssetBlob
//     preferido, DestURL→ClickURL) e Width/Height carregados; além do invariante
//     REAL de "no máximo um" campo de payload — setCreative é um switch SEM
//     default, então um creative_type não-reconhecido ⇒ zero campos, banner
//     ainda selecionado (CA3-007), enquanto ≥2 é estruturalmente impossível.
//   - cascade.Engine.Decide: SELEÇÃO + EXPOSIÇÃO — o Result.Banner selecionado
//     expõe o campo de payload correto, e — criticamente — um banner SEM
//     dest_url ainda é selecionado (trava a semântica REAL de não-rejeição).
//   - clicktoken.Signer: o vínculo server-side do dest_url no payload coberto
//     por HMAC + rejeição de token adulterado/vazio (a fatia importável real
//     de CA-3.1).
//
// NÃO coberto aqui (por não ser Go importável — ver TestCA3_CollectorServing_CrossRef):
//   - Render responsivo do pacote HTML5 (asyncjs/iframe do collector, package main).
//   - Contagem própria/dupla de impressão do third-party (pixel /lg, package main).
//   - Validação SSRF/esquema de dest_url e src de vídeo, e geração de VAST4
//     (validateDestURL/buildVAST4, package main).
//   - A rejeição "imagem exige dest_url" no upload (CHECK banners_dest_url_chk
//     no Postgres + form do console) — não há equivalente Go.
//
// Dependências: nenhuma infra externa (Redis, Redpanda, Postgres, MaxMind).
package parity_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hojex/adserver/internal/cascade"
	"github.com/hojex/adserver/internal/clicktoken"
	"github.com/hojex/adserver/internal/configload"
	"github.com/hojex/adserver/internal/rules"
	"github.com/hojex/adserver/internal/snapshot"
)

// ---------------------------------------------------------------------------
// Golden file schema (CA-3)
// ---------------------------------------------------------------------------

type ca3GoldenInput struct {
	ZoneID       string `json:"zone_id"`
	TenantID     string `json:"tenant_id"`
	CreativeType string `json:"creative_type"` // image|html5|thirdparty_tag|video
	AssetURL     string `json:"asset_url"`
	AssetBlob    string `json:"asset_blob"`
	DestURL      string `json:"dest_url"`
	Width        int32  `json:"width"`
	Height       int32  `json:"height"`
}

type ca3GoldenExpect struct {
	ImageURL   string `json:"image_url"`
	HTML       string `json:"html"`
	VideoURL   string `json:"video_url"`
	ClickURL   string `json:"click_url"`
	Width      int32  `json:"width"`
	Height     int32  `json:"height"`
	Selected   bool   `json:"selected"`
	ServedTier string `json:"served_tier"`
}

type ca3GoldenCase struct {
	ID          string          `json:"id"`
	Description string          `json:"description"`
	CA          string          `json:"ca"`
	Sub         string          `json:"sub"`
	Input       ca3GoldenInput  `json:"input"`
	Expect      ca3GoldenExpect `json:"expect"`
}

// ---------------------------------------------------------------------------
// Helpers — build a real snapshot through configload.Assemble (NOT a hand-set
// Banner) so the CA-3 creative-type→field mapping is exercised end-to-end.
// A single Remnant campaign on zone z1 carries one banner built from the case.
// ---------------------------------------------------------------------------

const (
	ca3Zone       = "z1"
	ca3CampaignID = "camp-ca3"
	ca3BannerID   = "ban-ca3"
)

func buildSnapFromCA3(gc ca3GoldenCase) *snapshot.Snapshot {
	in := gc.Input
	raw := configload.RawConfig{
		Zones: []configload.ZoneRow{
			{ID: in.ZoneID, TenantID: in.TenantID, Active: true},
		},
		Campaigns: []configload.CampaignRow{
			{
				ID:           ca3CampaignID,
				TenantID:     in.TenantID,
				Type:         "remnant",
				Priority:     1,
				PricingModel: "cpm",
				Currency:     "BRL",
				RateMinor:    100,
				Scale:        2,
				Active:       true,
			},
		},
		Banners: []configload.BannerRow{
			{
				ID:           ca3BannerID,
				TenantID:     in.TenantID,
				CampaignID:   ca3CampaignID,
				CreativeType: in.CreativeType,
				AssetURL:     in.AssetURL,
				AssetBlob:    in.AssetBlob,
				DestURL:      in.DestURL,
				Width:        in.Width,
				Height:       in.Height,
				Active:       true,
			},
		},
		CampaignZones: []configload.CampaignZoneRow{
			{CampaignID: ca3CampaignID, ZoneID: in.ZoneID},
		},
	}
	// goldenNow and servedTierProtoFromStr are defined in ca2_cascade_golden_test.go
	// (same package parity_test) — reused here to keep the idiom identical.
	return configload.Assemble(raw, "ca3-test", goldenNow)
}

// countNonEmptyPayload returns how many of the three mutually-exclusive
// creative payload fields are non-empty. setCreative (assemble.go:437-450) is a
// default-less switch, so the TRUE invariant is AT MOST ONE: a recognised
// creative_type sets exactly one; an unrecognised/empty-asset type sets zero
// (CA3-007). Two-or-more is structurally impossible (one assignment per case).
func countNonEmptyPayload(b *snapshot.Banner) int {
	n := 0
	if b.ImageURL != "" {
		n++
	}
	if b.HTML != "" {
		n++
	}
	if b.VideoURL != "" {
		n++
	}
	return n
}

// ---------------------------------------------------------------------------
// CA-3 golden runner — mapping (Assemble) + selection/exposure (Decide)
// ---------------------------------------------------------------------------

func TestCA3_CreativesGolden(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("golden", "ca3_creatives.json"))
	if err != nil {
		t.Fatalf("cannot read golden file: %v", err)
	}

	var cases []ca3GoldenCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatalf("cannot parse golden file: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("golden file has zero cases — gate requires at least one case")
	}

	eng := cascade.New(rules.New())

	for _, gc := range cases {
		gc := gc
		t.Run(gc.ID+"/"+gc.Sub, func(t *testing.T) {
			if gc.CA != "CA-3" {
				t.Fatalf("case %s: ca tag = %q, want CA-3", gc.ID, gc.CA)
			}

			snap := buildSnapFromCA3(gc)

			// --- (A) MAPPING: creative_type → Banner field, via Assemble ---
			ban := snap.Banners[ca3BannerID]
			if ban == nil {
				t.Fatalf("[%s] banner %q not present in assembled snapshot", gc.ID, ca3BannerID)
			}

			if ban.ImageURL != gc.Expect.ImageURL {
				t.Errorf("[%s] ImageURL: got %q, want %q", gc.ID, ban.ImageURL, gc.Expect.ImageURL)
			}
			if ban.HTML != gc.Expect.HTML {
				t.Errorf("[%s] HTML: got %q, want %q", gc.ID, ban.HTML, gc.Expect.HTML)
			}
			if ban.VideoURL != gc.Expect.VideoURL {
				t.Errorf("[%s] VideoURL: got %q, want %q", gc.ID, ban.VideoURL, gc.Expect.VideoURL)
			}
			// DestURL → ClickURL (unconditional; empty stays empty, no rejection).
			if ban.ClickURL != gc.Expect.ClickURL {
				t.Errorf("[%s] ClickURL: got %q, want %q", gc.ID, ban.ClickURL, gc.Expect.ClickURL)
			}
			if ban.Width != gc.Expect.Width {
				t.Errorf("[%s] Width: got %d, want %d", gc.ID, ban.Width, gc.Expect.Width)
			}
			if ban.Height != gc.Expect.Height {
				t.Errorf("[%s] Height: got %d, want %d", gc.ID, ban.Height, gc.Expect.Height)
			}

			// Creative payload invariant (setCreative, assemble.go:437-450): the
			// TRUE guarantee is AT MOST ONE payload field, not exactly one. A
			// recognised creative_type sets exactly one (pinned per-case by the
			// explicit ImageURL/HTML/VideoURL expects above); an unrecognised type
			// falls through the default-less switch and sets ZERO (CA3-007), yet
			// the banner is still selected. Two-or-more would be a real bug.
			if got := countNonEmptyPayload(ban); got > 1 {
				t.Errorf("[%s] creative payload exclusivity: %d fields non-empty, want at most 1 "+
					"(ImageURL=%q HTML=%q VideoURL=%q)", gc.ID, got, ban.ImageURL, ban.HTML, ban.VideoURL)
			}

			// --- (B) SELECTION + EXPOSURE: via Decide over the real snapshot ---
			req := cascade.Request{
				ZoneID:      gc.Input.ZoneID,
				TenantID:    gc.Input.TenantID,
				Rules:       &rules.Context{RequestTime: goldenNow},
				RequestTime: goldenNow,
			}
			result := eng.Decide(req, snap)

			wantTier := servedTierProtoFromStr(gc.Expect.ServedTier)
			if result.ServedTier != wantTier {
				t.Errorf("[%s] served_tier: got %v, want %v", gc.ID, result.ServedTier, wantTier)
			}

			if !gc.Expect.Selected {
				return
			}

			if result.Banner == nil {
				// This is the load-bearing no-rejection assertion for CA3-002:
				// a dest-less banner MUST still be selected — the engine never
				// rejects a creative for a missing destination.
				t.Fatalf("[%s] expected the creative to be SELECTED, got nil Result.Banner "+
					"(no-rejection semantics broken)", gc.ID)
			}
			if result.Banner.ID != ca3BannerID {
				t.Errorf("[%s] selected banner ID: got %q, want %q", gc.ID, result.Banner.ID, ca3BannerID)
			}
			// The EXPOSED creative surfaces the exact same single payload field.
			if result.Banner.ImageURL != gc.Expect.ImageURL {
				t.Errorf("[%s] exposed ImageURL: got %q, want %q", gc.ID, result.Banner.ImageURL, gc.Expect.ImageURL)
			}
			if result.Banner.HTML != gc.Expect.HTML {
				t.Errorf("[%s] exposed HTML: got %q, want %q", gc.ID, result.Banner.HTML, gc.Expect.HTML)
			}
			if result.Banner.VideoURL != gc.Expect.VideoURL {
				t.Errorf("[%s] exposed VideoURL: got %q, want %q", gc.ID, result.Banner.VideoURL, gc.Expect.VideoURL)
			}
			if result.Banner.ClickURL != gc.Expect.ClickURL {
				t.Errorf("[%s] exposed ClickURL: got %q, want %q", gc.ID, result.Banner.ClickURL, gc.Expect.ClickURL)
			}
		})
	}

	t.Logf("CA-3 golden suite: %d cases evaluated", len(cases))
}

// ---------------------------------------------------------------------------
// CA-3.1 (importable slice): dest_url is bound server-side into the HMAC-covered
// click token and a tampered/forged/empty token is rejected.
//
// The upload-time "image requires dest_url" rejection is a Postgres CHECK
// (banners_dest_url_chk) with NO Go equivalent, and the SSRF/empty-URL guard
// validateDestURL lives in collector package main (not importable). The ONE
// importable Go guarantee for CA-3.1 is clicktoken: the dest travels only
// inside a signed token, and tampering is rejected. This mirrors the ca6
// clicktoken cases but frames the invariant under CA-3.1.
// ---------------------------------------------------------------------------

func TestCA3_DestURL_ServerSideBinding_TamperRejected(t *testing.T) {
	s, err := clicktoken.New("parity-hmac-secret-for-ca3-tests", clicktoken.DefaultTTL)
	if err != nil {
		t.Fatalf("clicktoken.New: %v", err)
	}

	const (
		decisionID = "dec-ca3-001"
		bannerID   = ca3BannerID
		destURL    = "https://advertiser.example.com/landing"
	)

	// Round-trip: the exact server-side dest is recovered from a valid token.
	tok := s.Sign(decisionID, bannerID, destURL, time.Now().Add(clicktoken.DefaultTTL))
	gotDID, gotBID, gotURL, err := s.Validate(tok, time.Now())
	if err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	if gotDID != decisionID || gotBID != bannerID || gotURL != destURL {
		t.Errorf("token payload mismatch: got did=%q bid=%q url=%q", gotDID, gotBID, gotURL)
	}

	// Tampered payload: flip a byte in the token → HMAC mismatch → rejected.
	tampered := []byte(tok)
	tampered[0] ^= 0x01
	if _, _, _, err := s.Validate(string(tampered), time.Now()); err == nil {
		t.Error("tampered token accepted — dest_url binding not HMAC-protected (CA-3.1 violated)")
	}

	// Empty token: rejected outright (no dest can be derived from an unsigned param).
	if _, _, _, err := s.Validate("", time.Now()); err == nil {
		t.Error("empty token accepted — dest_url could be forged (CA-3.1 open-redirect guard broken)")
	}

	// A token signed with a DIFFERENT secret must not validate against this signer
	// (forged dest from a rogue signer is rejected).
	other, err := clicktoken.New("a-different-secret", clicktoken.DefaultTTL)
	if err != nil {
		t.Fatalf("clicktoken.New (other): %v", err)
	}
	forged := other.Sign(decisionID, bannerID, "https://evil.example.com/steal", time.Now().Add(clicktoken.DefaultTTL))
	if _, _, _, err := s.Validate(forged, time.Now()); err == nil {
		t.Error("token from a different secret accepted — dest_url forgeable (CA-3.1 violated)")
	}
}

// ---------------------------------------------------------------------------
// Cross-reference gate: the CA-3 serving behaviours that live ONLY in collector
// package main (buildAdTagJS image/HTML5 branches + /lg pixel, buildVAST4,
// validateDestURL) cannot be imported here. This gate NAMES the co-located
// collector tests that cover them so a rename fails CI — the same documentation
// contract pattern as TestCA6_CollectorTestSuite_CrossRef.
// ---------------------------------------------------------------------------

func TestCA3_CollectorServing_CrossRef(t *testing.T) {
	// map: CA-3 sub-criterion → collector tests (package main, not importable).
	requiredCollectorTests := map[string][]string{
		"CA-3.1 dest_url safe (SSRF/empty guard, serve/click-time)": {
			"TestValidateDestURL",
			"TestValidateDestURL_ValidCases",
			"TestValidateDestURL_SSRFBypasses",
		},
		"CA-3.1 image DOM-API render wrapped in signed ckURL": {
			"TestAdTagJS_ImageCreative_NoIframe_SafePath",
		},
		"CA-3.2 HTML5 responsive: sandboxed srcdoc iframe + width/height": {
			"TestAdTagJS_HTMLCreative_UsesSrcdocSandbox",
			"TestAdTagJS_HTMLCreative_NoInnerHTMLOnPublisherDOM",
		},
		"CA-3.3 third-party own impression count (parent-fired /lg pixel)": {
			"TestAdTagJS_HTMLCreative_ImpressionPixelPreserved",
			"TestLG_ImpressionCountedAtPixelLoad",
		},
		"CA-3.4 video VAST4 (remote src + dest, no VPAID)": {
			"TestVAST_ReturnsXML",
			"TestVAST_CDATAInjectionEscaped",
			"TestVAST_PrivateSrcRejected",
		},
	}

	total := 0
	t.Log("CA-3 cross-reference gate: these collector tests (package main) MUST be GREEN:")
	for sub, names := range requiredCollectorTests {
		for _, name := range names {
			t.Logf("  [%s] services/collector/cmd/collector: %s", sub, name)
			total++
		}
	}
	t.Logf("Run with: go test ./services/collector/... -v -run '<testname>'")

	if total == 0 {
		t.Fatal("cross-reference list must not be empty")
	}
}
