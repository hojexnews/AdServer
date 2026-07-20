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

import { useState } from "react";
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

  const createRule = trpc.cfg.deliveryRule.create.useMutation({
    onSuccess: () => {
      setSuccessMsg("Regras salvas com sucesso.");
      setShowWarning(false);
      setPendingValues(null);
    },
  });

  const {
    register,
    control,
    handleSubmit,
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

  // ---------------------------------------------------------------------------
  // Submit — roda anti-contradição CA-4 antes de salvar
  // A mesma lógica é reutilizada pelo copiloto (Fase 2) via checkDiffForContradictions
  // em use-copilot-session.ts: mesmo detectContradictions(), mesmo contrato.
  // ---------------------------------------------------------------------------
  const onSubmit = (values: RuleFormValues) => {
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

    // Sem contradição — salva diretamente
    void saveRules(values);
  };

  const saveRules = async (values: RuleFormValues) => {
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
      });
    }
  };

  const confirmDespiteConflicts = () => {
    if (pendingValues) {
      void saveRules(pendingValues);
    }
  };

  return (
    <div>
      <h1 className="text-2xl font-bold text-gray-900">
        Builder de Segmentação (§4.6)
      </h1>
      <p className="mt-1 text-sm text-gray-500">
        Configure regras AND/OR de entrega. Regras AND mutuamente exclusivas
        silenciam o banner — o sistema detecta contradições antes de salvar (CA-4).
        Regras sugeridas pela IA também passam pela mesma validação (Fase 2).
      </p>

      {/* Alerta de contradição (CA-4) */}
      {showWarning && contradictions.length > 0 && (
        <div
          role="alertdialog"
          aria-labelledby="contradiction-title"
          aria-describedby="contradiction-desc"
          className="mt-6 rounded-lg border-2 border-red-300 bg-red-50 p-4"
        >
          <h2
            id="contradiction-title"
            className="font-semibold text-red-900"
          >
            Contradição detectada nas regras (CA-4)
          </h2>
          <p id="contradiction-desc" className="mt-1 text-sm text-red-800">
            As regras abaixo são mutuamente exclusivas combinadas com AND.
            O banner NUNCA será exibido com esta combinação. Deseja salvar mesmo assim?
          </p>
          <ul className="mt-3 space-y-2">
            {contradictions.map((c, i) => (
              <li
                key={i}
                className="rounded bg-red-100 p-2 text-sm text-red-900"
              >
                {c.reason}
              </li>
            ))}
          </ul>
          <div className="mt-4 flex gap-3">
            <button
              onClick={() => {
                setShowWarning(false);
                setContradictions([]);
              }}
              className="rounded-md bg-white px-4 py-2 text-sm font-medium text-red-800 ring-1 ring-red-300 hover:bg-red-50 focus-visible:ring-2 focus-visible:ring-red-500"
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
          className="mt-4 rounded-md border border-green-200 bg-green-50 p-3 text-sm text-green-800"
        >
          {successMsg}
        </div>
      )}

      {createRule.error && (
        <ErrorState
          message={createRule.error.message}
        />
      )}

      <form
        onSubmit={handleSubmit(onSubmit)}
        aria-label="Formulário de regras de segmentação"
        className="mt-6 space-y-6"
        noValidate
      >
        {/* Owner */}
        <fieldset className="space-y-4 rounded-lg border border-gray-200 p-4">
          <legend className="px-1 text-sm font-semibold text-gray-700">
            Dono das regras
          </legend>

          <div className="flex gap-4">
            <div>
              <label
                htmlFor="ownerType"
                className="block text-sm font-medium text-gray-700"
              >
                Tipo
              </label>
              <select
                id="ownerType"
                {...register("ownerType")}
                className="mt-1 rounded-md border border-gray-300 px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500"
              >
                <option value="campaign">Campanha</option>
                <option value="banner">Banner</option>
              </select>
            </div>
            <div className="flex-1">
              <label
                htmlFor="ownerId"
                className="block text-sm font-medium text-gray-700"
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
                className="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500"
              />
              {errors.ownerId && (
                <p id="ownerId-error" role="alert" className="mt-1 text-xs text-red-600">
                  {errors.ownerId.message}
                </p>
              )}
            </div>
          </div>
        </fieldset>

        {/* Regras */}
        <fieldset className="space-y-3 rounded-lg border border-gray-200 p-4">
          <legend className="px-1 text-sm font-semibold text-gray-700">
            Regras de segmentação
          </legend>
          <p className="text-xs text-gray-500">
            Regras AND com o mesmo vetor e valores diferentes causam contradição
            e silenciam o banner. O sistema alerta antes de salvar.
          </p>

          {fields.map((field, index) => (
            <div
              key={field.id}
              className="flex flex-wrap items-end gap-3 rounded-md bg-gray-50 p-3"
            >
              {/* Operador lógico */}
              <div>
                <label
                  htmlFor={`rules.${index}.logicalOp`}
                  className="block text-xs font-medium text-gray-600"
                >
                  {index === 0 ? "Se" : "E / OU"}
                </label>
                <select
                  id={`rules.${index}.logicalOp`}
                  {...register(`rules.${index}.logicalOp`)}
                  disabled={index === 0}
                  className="mt-1 rounded border border-gray-300 px-2 py-1.5 text-sm focus-visible:ring-2 focus-visible:ring-brand-500 disabled:bg-gray-100"
                >
                  <option value="AND">E (AND)</option>
                  <option value="OR">OU (OR)</option>
                </select>
              </div>

              {/* Vetor */}
              <div>
                <label
                  htmlFor={`rules.${index}.vector`}
                  className="block text-xs font-medium text-gray-600"
                >
                  Vetor
                </label>
                <select
                  id={`rules.${index}.vector`}
                  {...register(`rules.${index}.vector`)}
                  className="mt-1 rounded border border-gray-300 px-2 py-1.5 text-sm focus-visible:ring-2 focus-visible:ring-brand-500"
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
                  className="block text-xs font-medium text-gray-600"
                >
                  Operador
                </label>
                <select
                  id={`rules.${index}.operator`}
                  {...register(`rules.${index}.operator`)}
                  className="mt-1 rounded border border-gray-300 px-2 py-1.5 text-sm focus-visible:ring-2 focus-visible:ring-brand-500"
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
                  className="block text-xs font-medium text-gray-600"
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
                  className="mt-1 w-full rounded border border-gray-300 px-2 py-1.5 text-sm focus-visible:ring-2 focus-visible:ring-brand-500"
                />
                {errors.rules?.[index]?.value && (
                  <p
                    id={`rule-${index}-value-error`}
                    role="alert"
                    className="mt-0.5 text-xs text-red-600"
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
                  className="rounded border border-red-200 bg-red-50 px-2 py-1.5 text-xs text-red-700 hover:bg-red-100 focus-visible:ring-2 focus-visible:ring-red-500"
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
            className="text-sm font-medium text-brand-600 hover:text-brand-700 focus-visible:ring-2 focus-visible:ring-brand-500"
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
