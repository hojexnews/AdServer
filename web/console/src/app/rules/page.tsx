"use client";

/**
 * Builder de segmentação (§4.6 / CA-4).
 *
 * Permite criar regras AND/OR para delivery de campanhas/banners.
 *
 * INVARIANTE CA-4: validação anti-contradição OBRIGATÓRIA antes de salvar.
 * Uma regra AND mutuamente exclusiva com outra silencia o banner.
 * O usuário é alertado com detalhes do conflito antes de confirmar.
 *
 * A validação anti-contradição roda:
 *   1. Sobre as regras digitadas pelo usuário (antes de salvar).
 *   2. Sobre regras SUGERIDAS pela IA (Fase 2): quando o copiloto propõe
 *      uma regra via WriteDiff, o HitlDiffPreview já exibe o aviso
 *      contradictionWarning antes de o usuário clicar em "Aplicar".
 *      O mesmo detectContradictions() é usado em ambos os caminhos.
 *
 * Usa RHF + Zod para o formulário.
 */

import { useEffect, useRef, useState } from "react";
import { useForm, useFieldArray } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { trpc } from "@/lib/trpc";
import {
  detectContradictions,
  SELECTABLE_VECTORS,
  type RuleCandidate,
} from "@/lib/contradiction";
import { LoadingState, ErrorState } from "@/components/ui/empty-state";

// ---------------------------------------------------------------------------
// Schema do formulário (Zod)
// ---------------------------------------------------------------------------

const RuleFormSchema = z.object({
  ownerType: z.enum(["campaign", "banner"]),
  ownerId: z.string().min(1, "Selecione uma campanha ou banner"),
  rules: z
    .array(
      z.object({
        // Fonte única: SELECTABLE_VECTORS (lib/contradiction.ts) — um vetor
        // novo aparece aqui E na checagem de exclusividade discreta ao mesmo
        // tempo, sem lista duplicada para dessincronizar (wave 30, achado
        // contradiction-vector-allowlist-gap).
        vector: z.enum(SELECTABLE_VECTORS),
        operator: z.enum([
          "is",
          "is not",
          "contains",
          "does not contain",
          "starts with",
          "ends with",
        ]),
        value: z.string().min(1, "Valor obrigatório"),
        logicalOp: z.enum(["AND", "OR"]),
        orderSeq: z.number().int().min(0),
      })
    )
    .min(1, "Adicione ao menos uma regra"),
});

type RuleFormValues = z.infer<typeof RuleFormSchema>;

const VECTORS = SELECTABLE_VECTORS;

const OPERATORS = [
  "is",
  "is not",
  "contains",
  "does not contain",
  "starts with",
  "ends with",
] as const;

export default function RulesPage() {
  const [contradictions, setContradictions] = useState<
    ReturnType<typeof detectContradictions>["conflicts"]
  >([]);
  const [showWarning, setShowWarning] = useState(false);
  const [pendingValues, setPendingValues] = useState<RuleFormValues | null>(
    null
  );
  const [successMsg, setSuccessMsg] = useState<string | null>(null);
  /**
   * Erro de persistência levantado por saveRules().
   *
   * FIX (31ª onda, rules-builder-ack-contradiction-never-sent): antes,
   * `void saveRules(...)` descartava a Promise. Qualquer rejeição (o CONFLICT
   * do backstop, uma falha de rede no meio do laço) sumia sem nenhum sinal:
   * o usuário via o formulário intacto, sem erro e sem sucesso, já com parte
   * das regras persistidas no servidor.
   */
  const [saveError, setSaveError] = useState<string | null>(null);
  const dialogRef = useRef<HTMLDivElement>(null);

  /**
   * FIX (31ª onda, rules-focus-restore-fires-mid-multirule-save): o
   * `onSuccess` desta mutação disparava UMA VEZ POR REGRA — `saveRules` é um
   * laço sequencial de `mutateAsync`. Efeito: assim que a PRIMEIRA regra era
   * gravada, o diálogo de contradição fechava e "Regras salvas com sucesso"
   * aparecia, enquanto as regras seguintes ainda estavam sendo enviadas (e
   * podiam falhar). Sucesso anunciado antes da hora, e — com a gestão de foco
   * introduzida nesta mesma onda — foco devolvido ao fundo no meio da operação.
   * As transições de fim de operação passam a acontecer UMA vez, depois do
   * laço inteiro, dentro de saveRules().
   */
  const createRule = trpc.cfg.deliveryRule.create.useMutation();

  const {
    register,
    control,
    handleSubmit,
    getValues,
    formState: { errors },
  } = useForm<RuleFormValues>({
    resolver: zodResolver(RuleFormSchema),
    defaultValues: {
      ownerType: "campaign",
      ownerId: "",
      rules: [
        {
          vector: "Geo - Country",
          operator: "is",
          value: "",
          logicalOp: "AND",
          orderSeq: 0,
        },
      ],
    },
  });

  const { fields, append, remove } = useFieldArray({
    control,
    name: "rules",
  });

  /**
   * Gestão de foco do alertdialog de contradição (WCAG 2.2 AA — SC 2.4.3
   * Focus Order, SC 2.1.2 No Keyboard Trap, SC 4.1.2 Name/Role/Value).
   *
   * FIX (31ª onda, a11y-rules-contradiction-alertdialog-no-focus-mgmt): o
   * bloco tinha role="alertdialog" mas nada mais de um diálogo — ao aparecer,
   * o foco permanecia no botão "Salvar" do formulário, ABAIXO do alerta na
   * ordem do DOM. Usuário de teclado/leitor de tela era avisado de uma
   * contradição bloqueante e precisava navegar PARA TRÁS às cegas para
   * encontrar os botões de decisão; usuário de leitor de tela podia nem saber
   * que o diálogo existia. Um `role="alertdialog"` sem foco movido para dentro
   * é uma promessa de semântica que a implementação não cumpre — o axe não
   * detecta isso (não há como inferir intenção de foco estaticamente).
   *
   * Ao abrir: foco vai para o contêiner do diálogo (tabIndex={-1}), que é o
   * início da região e cujo aria-labelledby/aria-describedby são anunciados.
   * Ao fechar: foco volta para o elemento que estava focado antes (restauração).
   * Escape: equivale a "Corrigir regras" (nunca prende o usuário).
   */
  const previouslyFocusedRef = useRef<HTMLElement | null>(null);

  const dismissWarning = () => {
    setShowWarning(false);
    setContradictions([]);
  };

  useEffect(() => {
    if (!showWarning) return;

    previouslyFocusedRef.current =
      document.activeElement instanceof HTMLElement
        ? document.activeElement
        : null;
    dialogRef.current?.focus();

    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        setShowWarning(false);
        setContradictions([]);
      }
    };
    document.addEventListener("keydown", onKeyDown);

    return () => {
      document.removeEventListener("keydown", onKeyDown);
      previouslyFocusedRef.current?.focus?.();
    };
  }, [showWarning]);

  // ---------------------------------------------------------------------------
  // Submit — roda anti-contradição CA-4 antes de salvar
  // A mesma lógica é reutilizada pelo copiloto (Fase 2) via checkDiffForContradictions
  // em use-copilot-session.ts: mesmo detectContradictions(), mesmo contrato.
  // ---------------------------------------------------------------------------
  const onSubmit = (values: RuleFormValues) => {
    // Um novo envio invalida o resultado do envio anterior — sem isto, o
    // usuário corrige as regras, reenvia, cai de novo no aviso de contradição
    // e continua vendo na tela a mensagem de erro da tentativa PASSADA como se
    // fosse desta (31ª onda, rules-saveerror-not-cleared-on-new-contradiction).
    setSaveError(null);
    setSuccessMsg(null);

    // CA-4: detectar contradições antes de salvar
    const candidates: RuleCandidate[] = values.rules.map((r) => ({
      vector: r.vector,
      operator: r.operator,
      value: r.value,
      logicalOp: r.logicalOp,
    }));

    const result = detectContradictions(candidates);

    if (result.hasContradiction) {
      // Mostra aviso — usuário precisa confirmar antes de salvar
      setContradictions(result.conflicts);
      setShowWarning(true);
      setPendingValues(values);
      return;
    }

    // Sem contradição — salva diretamente (sem reconhecimento: não há o que
    // reconhecer, e o backstop do servidor continua sendo a autoridade real).
    void saveRules(values, false);
  };

  /**
   * Persiste as regras do formulário.
   *
   * @param acknowledgeContradiction quando true, informa ao BFF que o usuário
   *   VIU e aceitou o aviso de contradição. Sem esta flag, o backstop
   *   server-side `assertNoContradiction` (bff/src/routers/config.ts:86)
   *   rejeita com CONFLICT — o input tem `acknowledgeContradiction:
   *   z.boolean().default(false)` (bff/src/schemas/config.ts:364).
   *
   * FIX (31ª onda): esta função NUNCA enviava a flag, então o botão
   * "Salvar mesmo assim (ciente do risco)" era funcionalmente morto — sempre
   * batia no CONFLICT. Pior: como o laço é sequencial e a contradição só
   * existe A PARTIR da segunda regra (o backstop compara a nova regra com as
   * JÁ PERSISTIDAS), a 1ª regra era gravada e a 2ª falhava, deixando o tenant
   * com um conjunto de regras PELA METADE — e a rejeição era engolida pelo
   * `void`, sem nenhuma mensagem na tela.
   */
  const saveRules = async (
    values: RuleFormValues,
    acknowledgeContradiction: boolean,
  ) => {
    setSaveError(null);
    setSuccessMsg(null);
    try {
      for (const rule of values.rules) {
        await createRule.mutateAsync({
          ownerType: values.ownerType,
          ownerId: values.ownerId,
          vector: rule.vector,
          operator: rule.operator,
          value: rule.value,
          logicalOp: rule.logicalOp,
          ruleSetId: null,
          orderSeq: rule.orderSeq,
          acknowledgeContradiction,
        });
      }
      // Só AQUI, com o laço inteiro concluído, a operação é um sucesso.
      setSuccessMsg("Regras salvas com sucesso.");
      setShowWarning(false);
      setContradictions([]);
      setPendingValues(null);
    } catch (err) {
      // Estado parcial é possível: as regras anteriores do laço já foram
      // persistidas. Dizemos isso explicitamente em vez de deixar o usuário
      // supor que nada foi salvo.
      const detail = err instanceof Error ? err.message : String(err);
      setSaveError(
        `Falha ao salvar as regras: ${detail}. ` +
          "Atenção: as regras anteriores desta lista podem já ter sido salvas — " +
          "revise as regras existentes deste dono antes de tentar de novo.",
      );
    }
  };

  const confirmDespiteConflicts = () => {
    if (!pendingValues) return;

    // O alertdialog NÃO é modal (aria-modal="false"): o formulário atrás dele
    // continua editável. `pendingValues` é um SNAPSHOT do momento em que o
    // aviso apareceu — se o usuário editar as regras com o diálogo aberto e
    // então clicar "Salvar mesmo assim", salvar o snapshot gravaria dados que
    // não são mais os da tela (31ª onda,
    // rules-stale-pendingvalues-editable-background).
    //
    // Salvamos SEMPRE o estado atual do formulário. E como o conjunto pode ter
    // mudado, reavaliamos a contradição: o `acknowledge` só vai junto se ainda
    // HOUVER contradição — se o usuário corrigiu as regras nesse meio-tempo,
    // salvamos pelo caminho normal, sem reconhecer nada que não existe mais.
    const current = getValues();
    const stillContradicts = detectContradictions(
      current.rules.map((r) => ({
        vector: r.vector,
        operator: r.operator,
        value: r.value,
        logicalOp: r.logicalOp,
      })),
    ).hasContradiction;

    void saveRules(current, stillContradicts);
  };

  return (
    <div>
      <h1 className="text-2xl font-bold text-foreground">
        Builder de Segmentação (§4.6)
      </h1>
      <p className="mt-1 text-sm text-muted-foreground">
        Configure regras AND/OR de entrega. Regras AND mutuamente exclusivas
        silenciam o banner — o sistema detecta contradições antes de salvar (CA-4).
        Regras sugeridas pela IA também passam pela mesma validação (Fase 2).
      </p>

      {/* Alerta de contradição (CA-4) */}
      {showWarning && contradictions.length > 0 && (
        <div
          ref={dialogRef}
          role="alertdialog"
          aria-modal="false"
          tabIndex={-1}
          aria-labelledby="contradiction-title"
          aria-describedby="contradiction-desc"
          className="mt-6 rounded-lg border-2 border-red-300 bg-red-50 p-4 focus-visible:ring-2 focus-visible:ring-red-500 dark:border-red-500/25 dark:bg-red-500/10"
        >
          <h2
            id="contradiction-title"
            className="font-semibold text-red-900 dark:text-red-200"
          >
            Contradição detectada nas regras (CA-4)
          </h2>
          <p id="contradiction-desc" className="mt-1 text-sm text-red-800 dark:text-red-300">
            As regras abaixo são mutuamente exclusivas combinadas com AND.
            O banner NUNCA será exibido com esta combinação. Deseja salvar mesmo assim?
          </p>
          <ul className="mt-3 space-y-2">
            {contradictions.map((c, i) => (
              <li
                key={i}
                className="rounded bg-red-100 p-2 text-sm text-red-900 dark:bg-red-500/15 dark:text-red-200"
              >
                {c.reason}
              </li>
            ))}
          </ul>
          <div className="mt-4 flex gap-3">
            <button
              onClick={dismissWarning}
              className="rounded-md bg-card px-4 py-2 text-sm font-medium text-red-800 ring-1 ring-red-300 hover:bg-red-50 focus-visible:ring-2 focus-visible:ring-red-500 dark:text-red-300 dark:ring-red-500/30 dark:hover:bg-red-500/10"
            >
              Corrigir regras
            </button>
            <button
              onClick={confirmDespiteConflicts}
              className="rounded-md bg-red-600 px-4 py-2 text-sm font-medium text-white hover:bg-red-700 focus-visible:ring-2 focus-visible:ring-red-500"
            >
              Salvar mesmo assim (ciente do risco)
            </button>
          </div>
        </div>
      )}

      {successMsg && (
        <div
          role="status"
          aria-live="polite"
          className="mt-4 rounded-md border border-green-200 bg-green-50 p-3 text-sm text-green-800 dark:border-green-500/25 dark:bg-green-500/10 dark:text-green-200"
        >
          {successMsg}
        </div>
      )}

      {/* Erro de persistência — inclui o aviso de estado parcial (ver saveRules).
          role="alert" para que leitores de tela anunciem sem depender de foco. */}
      {saveError && (
        <div role="alert" className="mt-4">
          <ErrorState message={saveError} />
        </div>
      )}

      <form
        onSubmit={handleSubmit(onSubmit)}
        aria-label="Formulário de regras de segmentação"
        className="mt-6 space-y-6"
        noValidate
      >
        {/* Owner */}
        <fieldset className="space-y-4 rounded-lg border border-border p-4">
          <legend className="px-1 text-sm font-semibold text-foreground">
            Dono das regras
          </legend>

          <div className="flex gap-4">
            <div>
              <label
                htmlFor="ownerType"
                className="block text-sm font-medium text-foreground"
              >
                Tipo
              </label>
              <select
                id="ownerType"
                {...register("ownerType")}
                className="mt-1 rounded-md border border-border px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500"
              >
                <option value="campaign">Campanha</option>
                <option value="banner">Banner</option>
              </select>
            </div>
            <div className="flex-1">
              <label
                htmlFor="ownerId"
                className="block text-sm font-medium text-foreground"
              >
                ID da campanha / banner
              </label>
              <input
                id="ownerId"
                type="text"
                inputMode="numeric"
                placeholder="ex.: 42"
                {...register("ownerId")}
                aria-describedby={errors.ownerId ? "ownerId-error" : undefined}
                className="mt-1 w-full rounded-md border border-border px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500"
              />
              {errors.ownerId && (
                <p id="ownerId-error" role="alert" className="mt-1 text-xs text-red-600 dark:text-red-300">
                  {errors.ownerId.message}
                </p>
              )}
            </div>
          </div>
        </fieldset>

        {/* Regras */}
        <fieldset className="space-y-3 rounded-lg border border-border p-4">
          <legend className="px-1 text-sm font-semibold text-foreground">
            Regras de segmentação
          </legend>
          <p className="text-xs text-muted-foreground">
            Regras AND com o mesmo vetor e valores diferentes causam contradição
            e silenciam o banner. O sistema alerta antes de salvar.
          </p>

          {fields.map((field, index) => (
            <div
              key={field.id}
              className="flex flex-wrap items-end gap-3 rounded-md bg-muted p-3"
            >
              {/* Operador lógico */}
              <div>
                <label
                  htmlFor={`rules.${index}.logicalOp`}
                  className="block text-xs font-medium text-muted-foreground"
                >
                  {index === 0 ? "Se" : "E / OU"}
                </label>
                <select
                  id={`rules.${index}.logicalOp`}
                  {...register(`rules.${index}.logicalOp`)}
                  disabled={index === 0}
                  className="mt-1 rounded border border-border px-2 py-1.5 text-sm focus-visible:ring-2 focus-visible:ring-brand-500 disabled:bg-muted"
                >
                  <option value="AND">E (AND)</option>
                  <option value="OR">OU (OR)</option>
                </select>
              </div>

              {/* Vetor */}
              <div>
                <label
                  htmlFor={`rules.${index}.vector`}
                  className="block text-xs font-medium text-muted-foreground"
                >
                  Vetor
                </label>
                <select
                  id={`rules.${index}.vector`}
                  {...register(`rules.${index}.vector`)}
                  className="mt-1 rounded border border-border px-2 py-1.5 text-sm focus-visible:ring-2 focus-visible:ring-brand-500"
                >
                  {VECTORS.map((v) => (
                    <option key={v} value={v}>
                      {v}
                    </option>
                  ))}
                </select>
              </div>

              {/* Operador */}
              <div>
                <label
                  htmlFor={`rules.${index}.operator`}
                  className="block text-xs font-medium text-muted-foreground"
                >
                  Operador
                </label>
                <select
                  id={`rules.${index}.operator`}
                  {...register(`rules.${index}.operator`)}
                  className="mt-1 rounded border border-border px-2 py-1.5 text-sm focus-visible:ring-2 focus-visible:ring-brand-500"
                >
                  {OPERATORS.map((op) => (
                    <option key={op} value={op}>
                      {op}
                    </option>
                  ))}
                </select>
              </div>

              {/* Valor */}
              <div className="flex-1">
                <label
                  htmlFor={`rules.${index}.value`}
                  className="block text-xs font-medium text-muted-foreground"
                >
                  Valor
                </label>
                <input
                  id={`rules.${index}.value`}
                  type="text"
                  placeholder="ex.: monday, BR, mobile"
                  {...register(`rules.${index}.value`)}
                  aria-describedby={
                    errors.rules?.[index]?.value
                      ? `rule-${index}-value-error`
                      : undefined
                  }
                  className="mt-1 w-full rounded border border-border px-2 py-1.5 text-sm focus-visible:ring-2 focus-visible:ring-brand-500"
                />
                {errors.rules?.[index]?.value && (
                  <p
                    id={`rule-${index}-value-error`}
                    role="alert"
                    className="mt-0.5 text-xs text-red-600 dark:text-red-300"
                  >
                    {errors.rules[index]?.value?.message}
                  </p>
                )}
              </div>

              {/* Remover */}
              {fields.length > 1 && (
                <button
                  type="button"
                  onClick={() => remove(index)}
                  aria-label={`Remover regra ${index + 1}`}
                  className="rounded border border-red-200 bg-red-50 px-2 py-1.5 text-xs text-red-700 hover:bg-red-100 focus-visible:ring-2 focus-visible:ring-red-500 dark:border-red-500/25 dark:bg-red-500/10 dark:text-red-300 dark:hover:bg-red-500/15"
                >
                  Remover
                </button>
              )}
            </div>
          ))}

          <button
            type="button"
            onClick={() =>
              append({
                vector: "Geo - Country",
                operator: "is",
                value: "",
                logicalOp: "AND",
                orderSeq: fields.length,
              })
            }
            className="text-sm font-medium text-brand-600 hover:text-brand-700 dark:text-brand-300 dark:hover:text-brand-300 focus-visible:ring-2 focus-visible:ring-brand-500"
          >
            + Adicionar regra
          </button>
        </fieldset>

        <button
          type="submit"
          disabled={createRule.isPending}
          className="rounded-md bg-brand-600 px-6 py-2.5 text-sm font-semibold text-white hover:bg-brand-700 focus-visible:ring-2 focus-visible:ring-brand-500 disabled:opacity-50"
          aria-busy={createRule.isPending}
        >
          {createRule.isPending ? "Salvando..." : "Salvar regras"}
        </button>
      </form>
    </div>
  );
}

// Silence unused import warning for LoadingState
void LoadingState;
