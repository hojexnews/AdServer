// ml/features/go/parity_contract.go
//
// Contrato de paridade Go para a spec de featurizacao (J1).
//
// ATENCAO: Este arquivo documenta a INTERFACE que internal/ranker/featurize.go
// (J1) deve implementar. NAO e um pacote Go importavel — ele fica em ml/
// que e fora do go.mod e serve como referencia para o engenheiro de J1.
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
//   Go:     github.com/spaolacci/murmur3
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
// import (
//     "time"
//     "github.com/hojex/adserver/gen/go/adserver/common/v1"
//     "github.com/hojex/adserver/internal/cascade"
//     "github.com/hojex/adserver/internal/snapshot"
// )
//
// const FeatureSpecVersion = "1.0.0"
// const FeatureVectorLength = 23
//
// type FeaturizeInput struct {
//     Zone            *snapshot.Zone
//     RequestTime     time.Time           // UTC
//     Geo             *commonv1.Geo       // pais+cidade, sem IP (TX-5)
//     DeviceClass     string              // useragent.Classify() output
//     Candidate       *cascade.Candidate
//     CandidateCount  uint32
//     CampaignDeliveredImpressions int64
//     CampaignGoalImpressions      int64
//     CampaignDeliveredClicks      int64
// }
//
// // Featurize transforma um FeaturizeInput em um vetor float32 de comprimento 23.
// // REGRAS:
// //   - Sem alocacao de rede; todas as fontes sao in-memory (TX-4).
// //   - ecpm_minor_units entra como int64, nao float (TX-2/DA-10).
// //   - Geo deriva do commonv1.Geo — IP bruto nunca entra aqui (TX-5/DA-11).
// //   - Hash: murmur3.SeedSum32(seed, []byte(value)) % numBuckets
// //   - Tolerancia de paridade com Python: abs(go - py) <= 1e-9
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
//             require.LessOrEqual(t, diff, 1e-9,
//                 "caso %s indice %d: got %v want %v diff %v",
//                 tc.ID, i, actual[i], expected[i], diff)
//         }
//     }
// }
// -------------------------------------------------------------------

package features_go_contract
// Este pacote e INTENCIONAL MENTE vazio — serve so como documentacao.
// Nao importar; nao faz parte do go.mod.
