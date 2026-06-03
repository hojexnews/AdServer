"use client";

import { useState } from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { trpc } from "@/lib/trpc";
import { MoneyDisplay } from "@/components/ui/money-display";
import { LoadingState, ErrorState, EmptyState } from "@/components/ui/empty-state";

/**
 * Rate (preço) é string DECIMAL + currency no formulário.
 * NUNCA aritmética monetária no cliente (TX-2/DA-10).
 * O BFF entrega e recebe rate como { amount: string, currency: string }.
 */
const CreateSchema = z
  .object({
    advertiserId: z.string().min(1, "ID do anunciante obrigatório"),
    name: z.string().min(1, "Nome obrigatório"),
    type: z.enum(["override", "contract", "remnant"]),
    priority: z.coerce.number().int().min(1).max(10),
    pricingModel: z.enum(["cpm", "cpc", "cpa", "tenancy"]),
    goalTarget: z.coerce.number().int().positive().nullable(),
    goalMetric: z.enum(["impressions", "clicks", "conversions"]).nullable(),
    startAt: z.string().min(1, "Data de início obrigatória"),
    endAt: z.string().min(1, "Data de fim obrigatória"),
    // rate como string DECIMAL — nunca Number (TX-2)
    rateAmount: z
      .string()
      .regex(/^\d+(\.\d+)?$/, "Rate deve ser decimal positivo (ex.: 5.00)"),
    rateCurrency: z
      .string()
      .min(1)
      .max(10)
      .regex(/^[A-Z0-9]+$/, "Currency em maiúsculas (ex.: BRL, USD, USDC)"),
  })
  .refine((d) => d.endAt > d.startAt, {
    message: "Data de fim deve ser posterior ao início",
    path: ["endAt"],
  })
  .refine(
    (d) => {
      if (d.pricingModel === "tenancy")
        return d.goalTarget === null && d.goalMetric === null;
      return d.goalTarget !== null && d.goalMetric !== null;
    },
    { message: "Tenancy não tem meta; outros modelos exigem goalTarget + goalMetric" }
  );

type CreateValues = z.infer<typeof CreateSchema>;

export default function CampaignsPage() {
  const [showForm, setShowForm] = useState(false);
  const utils = trpc.useUtils();

  const { data, isLoading, error, refetch } =
    trpc.cfg.campaign.list.useQuery({});

  const create = trpc.cfg.campaign.create.useMutation({
    onSuccess: () => {
      void utils.cfg.campaign.list.invalidate();
      setShowForm(false);
    },
  });

  const {
    register,
    handleSubmit,
    watch,
    formState: { errors },
    reset,
  } = useForm<CreateValues>({
    resolver: zodResolver(CreateSchema),
    defaultValues: {
      type: "remnant",
      priority: 5,
      pricingModel: "cpm",
      goalTarget: null,
      goalMetric: null,
      rateAmount: "0.00",
      rateCurrency: "BRL",
    },
  });

  const pricingModel = watch("pricingModel");

  if (isLoading) return <LoadingState label="Carregando campanhas..." />;
  if (error) return <ErrorState message={error.message} retry={() => { void refetch(); }} />;

  return (
    <div>
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold text-gray-900">Campanhas</h1>
        <button
          onClick={() => setShowForm((v) => !v)}
          className="rounded-md bg-brand-600 px-4 py-2 text-sm font-semibold text-white hover:bg-brand-700 focus-visible:ring-2 focus-visible:ring-brand-500"
        >
          {showForm ? "Cancelar" : "Nova campanha"}
        </button>
      </div>

      {showForm && (
        <form
          onSubmit={handleSubmit((v) => {
            // Normaliza datas para ISO-8601 com timezone
            void create.mutate({
              ...v,
              startAt: new Date(v.startAt).toISOString(),
              endAt: new Date(v.endAt).toISOString(),
            });
          })}
          aria-label="Criar campanha"
          noValidate
          className="mt-4 space-y-4 rounded-lg border border-gray-200 bg-white p-6"
        >
          <h2 className="font-semibold text-gray-800">Nova campanha</h2>

          <div className="grid grid-cols-2 gap-4">
            <div>
              <label htmlFor="advertiserId" className="block text-sm font-medium text-gray-700">
                ID do Anunciante
              </label>
              <input
                id="advertiserId"
                type="text"
                inputMode="numeric"
                {...register("advertiserId")}
                className="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500"
              />
              {errors.advertiserId && (
                <p role="alert" className="mt-1 text-xs text-red-600">{errors.advertiserId.message}</p>
              )}
            </div>
            <div>
              <label htmlFor="campName" className="block text-sm font-medium text-gray-700">
                Nome
              </label>
              <input
                id="campName"
                type="text"
                {...register("name")}
                className="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500"
              />
              {errors.name && (
                <p role="alert" className="mt-1 text-xs text-red-600">{errors.name.message}</p>
              )}
            </div>
          </div>

          <div className="grid grid-cols-3 gap-4">
            <div>
              <label htmlFor="type" className="block text-sm font-medium text-gray-700">Tipo</label>
              <select id="type" {...register("type")} className="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500">
                <option value="override">Override</option>
                <option value="contract">Contract</option>
                <option value="remnant">Remnant</option>
              </select>
            </div>
            <div>
              <label htmlFor="priority" className="block text-sm font-medium text-gray-700">Prioridade (1-10)</label>
              <input id="priority" type="number" min={1} max={10} {...register("priority")} className="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500" />
            </div>
            <div>
              <label htmlFor="pricingModel" className="block text-sm font-medium text-gray-700">Modelo</label>
              <select id="pricingModel" {...register("pricingModel")} className="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500">
                <option value="cpm">CPM</option>
                <option value="cpc">CPC</option>
                <option value="cpa">CPA</option>
                <option value="tenancy">Tenancy</option>
              </select>
            </div>
          </div>

          {/* Rate — string DECIMAL, nunca Number (TX-2) */}
          <div className="grid grid-cols-2 gap-4">
            <div>
              <label htmlFor="rateAmount" className="block text-sm font-medium text-gray-700">
                Rate (valor decimal, ex.: 5.00)
              </label>
              <input
                id="rateAmount"
                type="text"
                inputMode="decimal"
                placeholder="ex.: 12.50"
                {...register("rateAmount")}
                className="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500"
              />
              {errors.rateAmount && (
                <p role="alert" className="mt-1 text-xs text-red-600">{errors.rateAmount.message}</p>
              )}
            </div>
            <div>
              <label htmlFor="rateCurrency" className="block text-sm font-medium text-gray-700">
                Moeda (ex.: BRL, USD, USDC)
              </label>
              <input
                id="rateCurrency"
                type="text"
                placeholder="BRL"
                {...register("rateCurrency")}
                className="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500"
              />
              {errors.rateCurrency && (
                <p role="alert" className="mt-1 text-xs text-red-600">{errors.rateCurrency.message}</p>
              )}
            </div>
          </div>

          {pricingModel !== "tenancy" && (
            <div className="grid grid-cols-2 gap-4">
              <div>
                <label htmlFor="goalTarget" className="block text-sm font-medium text-gray-700">Goal Target</label>
                <input id="goalTarget" type="number" min={1} {...register("goalTarget", { setValueAs: (v: string) => v === "" ? null : parseInt(v, 10) })} className="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500" />
              </div>
              <div>
                <label htmlFor="goalMetric" className="block text-sm font-medium text-gray-700">Goal Metric</label>
                <select id="goalMetric" {...register("goalMetric")} className="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500">
                  <option value="impressions">Impressões</option>
                  <option value="clicks">Cliques</option>
                  <option value="conversions">Conversões</option>
                </select>
              </div>
            </div>
          )}

          <div className="grid grid-cols-2 gap-4">
            <div>
              <label htmlFor="startAt" className="block text-sm font-medium text-gray-700">Início</label>
              <input id="startAt" type="datetime-local" {...register("startAt")} className="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500" />
              {errors.startAt && <p role="alert" className="mt-1 text-xs text-red-600">{errors.startAt.message}</p>}
            </div>
            <div>
              <label htmlFor="endAt" className="block text-sm font-medium text-gray-700">Fim</label>
              <input id="endAt" type="datetime-local" {...register("endAt")} className="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500" />
              {errors.endAt && <p role="alert" className="mt-1 text-xs text-red-600">{errors.endAt.message}</p>}
            </div>
          </div>

          {create.error && <ErrorState message={create.error.message} />}

          <div className="flex gap-3">
            <button type="submit" disabled={create.isPending} aria-busy={create.isPending} className="rounded-md bg-brand-600 px-4 py-2 text-sm font-semibold text-white hover:bg-brand-700 focus-visible:ring-2 focus-visible:ring-brand-500 disabled:opacity-50">
              {create.isPending ? "Salvando..." : "Criar"}
            </button>
            <button type="button" onClick={() => { setShowForm(false); reset(); }} className="rounded-md border border-gray-300 px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 focus-visible:ring-2 focus-visible:ring-brand-500">
              Cancelar
            </button>
          </div>
        </form>
      )}

      <div className="mt-6">
        {data?.length === 0 ? (
          <EmptyState title="Nenhuma campanha" description="Crie a primeira campanha." />
        ) : (
          <table className="w-full border-collapse rounded-lg border border-gray-200 bg-white text-sm" aria-label="Lista de campanhas">
            <thead>
              <tr className="border-b border-gray-200 bg-gray-50">
                <th scope="col" className="px-4 py-3 text-left font-semibold text-gray-700">ID</th>
                <th scope="col" className="px-4 py-3 text-left font-semibold text-gray-700">Nome</th>
                <th scope="col" className="px-4 py-3 text-left font-semibold text-gray-700">Tipo</th>
                <th scope="col" className="px-4 py-3 text-left font-semibold text-gray-700">Modelo</th>
                <th scope="col" className="px-4 py-3 text-left font-semibold text-gray-700">Rate</th>
                <th scope="col" className="px-4 py-3 text-left font-semibold text-gray-700">Ativo</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              {data?.map((c) => (
                <tr key={c.id} className="hover:bg-gray-50">
                  <td className="px-4 py-3 font-mono text-xs text-gray-500">{c.id}</td>
                  <td className="px-4 py-3 font-medium text-gray-900">{c.name}</td>
                  <td className="px-4 py-3 capitalize text-gray-600">{c.type}</td>
                  <td className="px-4 py-3 uppercase text-gray-600">{c.pricingModel}</td>
                  <td className="px-4 py-3">
                    {/* MoneyDisplay — NUNCA Number para dinheiro (TX-2) */}
                    <MoneyDisplay money={c.rate} />
                  </td>
                  <td className="px-4 py-3">
                    <span className={`inline-flex rounded-full px-2 py-0.5 text-xs font-medium ${c.active ? "bg-green-100 text-green-800" : "bg-gray-100 text-gray-600"}`}>
                      {c.active ? "Ativo" : "Inativo"}
                    </span>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}
