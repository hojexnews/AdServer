// ml/features/go/parity_contract.go
//
// Contrato de paridade Go para a spec de featurizacao (J1).
//
// ATENCAO: Este arquivo documenta a INTERFACE que internal/ranker/featurize.go
// (J1) deve implementar. E um pacote Go valido dentro do modulo unico
// (github.com/hojex/adserver/ml/features/go) e compila no gate make go-build.
// Seu papel e DOCUMENTAL/CONTRATUAL — a spec canonica de featurizacao anti-skew
// espelhada Go<->Python. NAO deve ser importado pelo hot path do motor de decisao.
//
// O teste de paridade Go real fica em:
//   internal/ranker/parity_test.go  (J1)
//
// Que carrega o mesmo arquivo de fixtures:
//   ml/features/testdata/parity_cases.json
//
// e verifica que FeaturizeInput -> []float32 produz o mesmo vetor que
// a implementacao Python (ml/features/python/featurize.py).
//
// -------------------------------------------------------------------
// HASH CANONICO (CRITICO PARA PARIDADE):
//   Go:     github.com/twmb/murmur3
//           murmur3.SeedSum32(uint32(seed), []byte(value)) % uint32(numBuckets)
//
//   Python: mmh3
//           mmh3.hash(value, seed, signed=False) % num_buckets
//
//   Os dois DEVEM produzir o mesmo uint32 para o mesmo (value, seed).
//   Verificado pelo teste de paridade com fixtures de hash explicitos.
// -------------------------------------------------------------------
//
// ASSINATURA ESPERADA de internal/ranker/featurize.go:
//
// package ranker
//
// import "time"
//
// const FeatureSpecVersion = "1.0.0"
// const FeatureVectorLength = 23
//
// type FeaturizeInput struct {
//     // Zone dimensions (snapshot.Zone — campos planos, sem dependencia externa).
//     ZoneWidth  int32
//     ZoneHeight int32
//
//     // Request time (UTC).
//     RequestTime time.Time
//
//     // Geo: country (ISO 3166-1 alpha-2) e city name. Vazio = ausente.
//     // IP bruto NUNCA presente aqui (TX-5/DA-11).
//     GeoCountry string
//     GeoCity    string
//
//     // DeviceClass e a classe grosseira de useragent.Classify.
//     // UA bruto descartado upstream (TX-5/DA-11).
//     DeviceClass string
//
//     // Candidate fields (cascade.Candidate — campos planos).
//     CandidateTier    int     // 1=Override, 2=Contract, 3=Remnant
//     CampaignPriority int32
//     PacingDeficit    float64 // [0,1]; 0 para nao-Contract
//
//     // ECPMMinorUnits e o eCPM em minor-units (int64, TX-2).
//     // 0 para Override/Contract.
//     ECPMMinorUnits int64
//
//     // Banner dimensions.
//     BannerWidth  int32
//     BannerHeight int32
//
//     // CreativeType: "image", "html", "video", ou "unknown".
//     CreativeType string
//
//     // CandidateCount e o total de candidatos elegiveis no estrato.
//     CandidateCount uint32
//
//     // Historico de campanha (contadores aproximados do snapshot).
//     CampaignDeliveredImpressions int64
//     CampaignGoalImpressions      int64
//     CampaignDeliveredClicks      int64
// }
//
// // Featurize transforma um FeaturizeInput em um vetor float32 de comprimento 23.
// // REGRAS:
// //   - Sem alocacao de rede; todas as fontes sao in-memory (TX-4).
// //   - ecpm_minor_units entra como int64, nao float (TX-2/DA-10).
// //   - Geo: campos GeoCountry/GeoCity — IP bruto nunca entra aqui (TX-5/DA-11).
// //   - Hash: murmur3.SeedSum32(seed, []byte(value)) % numBuckets
// //   - Tolerancia de paridade com Python: abs(go - py) <= 1e-6
// //     (vetor e float32; cast float32<->float64 introduz ruido ~1.19e-7;
// //      1e-6 captura bugs reais absorvendo esse ruido; 1e-9 e impossivel de
// //      satisfazer consistentemente com float32.)
// //
// func Featurize(inp FeaturizeInput) [FeatureVectorLength]float32 {
//     // ... implementacao em J1 ...
// }
//
// -------------------------------------------------------------------
// ESTRUTURA DO TESTE DE PARIDADE Go (internal/ranker/parity_test.go):
//
// func TestParityFromFixtures(t *testing.T) {
//     data, err := os.ReadFile("../../ml/features/testdata/parity_cases.json")
//     // ... parse JSON ...
//     require.Equal(t, FeatureSpecVersion, fixtures.FeatureSpecVersion)
//     for _, tc := range fixtures.Cases {
//         inp := buildInput(tc)
//         actual := Featurize(inp)
//         expected := tc.ExpectedVectorComputed
//         for i := 0; i < FeatureVectorLength; i++ {
//             diff := math.Abs(float64(actual[i]) - expected[i])
//             require.LessOrEqual(t, diff, 1e-6,
//                 "caso %s indice %d: got %v want %v diff %v",
//                 tc.ID, i, actual[i], expected[i], diff)
//         }
//     }
// }
// -------------------------------------------------------------------

package features_go_contract
// Este pacote e intencionalmente vazio — serve so como documentacao/contrato.
// Nao importar no hot path; ver cabecalho acima.
