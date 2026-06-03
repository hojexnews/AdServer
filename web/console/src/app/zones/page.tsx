"use client";

import { useState } from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { trpc } from "@/lib/trpc";
import { LoadingState, ErrorState, EmptyState } from "@/components/ui/empty-state";

const CreateSchema = z.object({
  siteId: z.string().min(1, "ID do site obrigatório"),
  name: z.string().min(1, "Nome obrigatório"),
  width: z.coerce.number().int().positive("Largura positiva"),
  height: z.coerce.number().int().positive("Altura positiva"),
  type: z.string().min(1),
});
type CreateValues = z.infer<typeof CreateSchema>;

const LinkSchema = z.object({
  campaignId: z.string().min(1, "ID da campanha obrigatório"),
  zoneIds: z.string().min(1, "IDs de zonas obrigatório"),
});
type LinkValues = z.infer<typeof LinkSchema>;

export default function ZonesPage() {
  const [showCreateForm, setShowCreateForm] = useState(false);
  const [showLinkForm, setShowLinkForm] = useState(false);
  const [linkCampaignId, setLinkCampaignId] = useState<string | null>(null);
  const utils = trpc.useUtils();

  const { data, isLoading, error, refetch } =
    trpc.cfg.zone.list.useQuery({});

  const create = trpc.cfg.zone.create.useMutation({
    onSuccess: () => {
      void utils.cfg.zone.list.invalidate();
      setShowCreateForm(false);
    },
  });

  const link = trpc.cfg.campaignZone.link.useMutation({
    onSuccess: () => {
      setShowLinkForm(false);
      setLinkCampaignId(null);
    },
  });

  const { data: linkedZones } = trpc.cfg.campaignZone.list.useQuery(
    { campaignId: linkCampaignId ?? "" },
    { enabled: linkCampaignId !== null }
  );

  const createForm = useForm<CreateValues>({
    resolver: zodResolver(CreateSchema),
    defaultValues: { type: "standard" },
  });

  const linkForm = useForm<LinkValues>({
    resolver: zodResolver(LinkSchema),
  });

  if (isLoading) return <LoadingState label="Carregando zonas..." />;
  if (error) return <ErrorState message={error.message} retry={() => { void refetch(); }} />;

  return (
    <div>
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold text-gray-900">Zonas de inventário</h1>
        <div className="flex gap-2">
          <button
            onClick={() => setShowLinkForm((v) => !v)}
            className="rounded-md border border-brand-600 px-4 py-2 text-sm font-semibold text-brand-600 hover:bg-brand-50 focus-visible:ring-2 focus-visible:ring-brand-500"
          >
            Vincular campanha ↔ zona
          </button>
          <button
            onClick={() => setShowCreateForm((v) => !v)}
            className="rounded-md bg-brand-600 px-4 py-2 text-sm font-semibold text-white hover:bg-brand-700 focus-visible:ring-2 focus-visible:ring-brand-500"
          >
            Nova zona
          </button>
        </div>
      </div>

      {/* Formulário de criação de zona */}
      {showCreateForm && (
        <form
          onSubmit={createForm.handleSubmit((v) => void create.mutate(v))}
          aria-label="Criar zona"
          noValidate
          className="mt-4 space-y-4 rounded-lg border border-gray-200 bg-white p-6"
        >
          <h2 className="font-semibold text-gray-800">Nova zona</h2>
          <div className="grid grid-cols-2 gap-4">
            <div>
              <label htmlFor="siteId" className="block text-sm font-medium text-gray-700">ID do Site</label>
              <input id="siteId" type="text" inputMode="numeric" {...createForm.register("siteId")} className="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500" />
              {createForm.formState.errors.siteId && <p role="alert" className="mt-1 text-xs text-red-600">{createForm.formState.errors.siteId.message}</p>}
            </div>
            <div>
              <label htmlFor="zoneName" className="block text-sm font-medium text-gray-700">Nome</label>
              <input id="zoneName" type="text" {...createForm.register("name")} className="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500" />
              {createForm.formState.errors.name && <p role="alert" className="mt-1 text-xs text-red-600">{createForm.formState.errors.name.message}</p>}
            </div>
            <div>
              <label htmlFor="zoneWidth" className="block text-sm font-medium text-gray-700">Largura (px)</label>
              <input id="zoneWidth" type="number" min={1} {...createForm.register("width")} className="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500" />
            </div>
            <div>
              <label htmlFor="zoneHeight" className="block text-sm font-medium text-gray-700">Altura (px)</label>
              <input id="zoneHeight" type="number" min={1} {...createForm.register("height")} className="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500" />
            </div>
          </div>
          {create.error && <ErrorState message={create.error.message} />}
          <div className="flex gap-3">
            <button type="submit" disabled={create.isPending} aria-busy={create.isPending} className="rounded-md bg-brand-600 px-4 py-2 text-sm font-semibold text-white hover:bg-brand-700 focus-visible:ring-2 focus-visible:ring-brand-500 disabled:opacity-50">
              {create.isPending ? "Salvando..." : "Criar"}
            </button>
            <button type="button" onClick={() => setShowCreateForm(false)} className="rounded-md border border-gray-300 px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50">
              Cancelar
            </button>
          </div>
        </form>
      )}

      {/* Formulário de vínculo N:N campanha ↔ zona (DA-2 / CA-1) */}
      {showLinkForm && (
        <div className="mt-4 rounded-lg border border-blue-200 bg-blue-50 p-6">
          <h2 className="font-semibold text-blue-900">
            Vínculo N:N campanha ↔ zona (DA-2)
          </h2>
          <p className="mt-1 text-sm text-blue-700">
            Uma campanha pode estar vinculada a múltiplas zonas. O motor de
            decisão avalia o vínculo dinamicamente por request.
          </p>

          <form
            onSubmit={linkForm.handleSubmit((v) => {
              const ids = v.zoneIds.split(",").map((s) => s.trim()).filter(Boolean);
              void link.mutate({ campaignId: v.campaignId, zoneIds: ids });
              setLinkCampaignId(v.campaignId);
            })}
            aria-label="Vincular campanha a zonas"
            noValidate
            className="mt-4 space-y-4"
          >
            <div>
              <label htmlFor="linkCampaignId" className="block text-sm font-medium text-blue-900">
                ID da Campanha
              </label>
              <input
                id="linkCampaignId"
                type="text"
                inputMode="numeric"
                {...linkForm.register("campaignId")}
                onChange={(e) => {
                  linkForm.setValue("campaignId", e.target.value);
                  setLinkCampaignId(e.target.value || null);
                }}
                className="mt-1 w-full rounded-md border border-blue-300 bg-white px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-blue-500"
              />
              {linkForm.formState.errors.campaignId && (
                <p role="alert" className="mt-1 text-xs text-red-600">{linkForm.formState.errors.campaignId.message}</p>
              )}
            </div>

            {/* Zonas já vinculadas */}
            {linkedZones && linkedZones.length > 0 && (
              <div>
                <p className="text-xs font-medium text-blue-800">
                  Zonas já vinculadas:
                </p>
                <ul className="mt-1 flex flex-wrap gap-1">
                  {linkedZones.map((cz) => (
                    <li key={cz.zoneId} className="rounded bg-blue-200 px-2 py-0.5 text-xs text-blue-900">
                      Zona #{cz.zoneId}
                    </li>
                  ))}
                </ul>
              </div>
            )}

            <div>
              <label htmlFor="linkZoneIds" className="block text-sm font-medium text-blue-900">
                IDs de zonas a vincular (separados por vírgula)
              </label>
              <input
                id="linkZoneIds"
                type="text"
                placeholder="ex.: 1, 2, 3"
                {...linkForm.register("zoneIds")}
                className="mt-1 w-full rounded-md border border-blue-300 bg-white px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-blue-500"
              />
            </div>

            {link.error && <ErrorState message={link.error.message} />}

            <div className="flex gap-3">
              <button
                type="submit"
                disabled={link.isPending}
                aria-busy={link.isPending}
                className="rounded-md bg-blue-600 px-4 py-2 text-sm font-semibold text-white hover:bg-blue-700 focus-visible:ring-2 focus-visible:ring-blue-500 disabled:opacity-50"
              >
                {link.isPending ? "Vinculando..." : "Vincular"}
              </button>
              <button type="button" onClick={() => { setShowLinkForm(false); setLinkCampaignId(null); }} className="rounded-md border border-blue-300 px-4 py-2 text-sm font-medium text-blue-800 hover:bg-blue-100">
                Fechar
              </button>
            </div>
          </form>
        </div>
      )}

      <div className="mt-6">
        {data?.length === 0 ? (
          <EmptyState title="Nenhuma zona cadastrada" description="Crie a primeira zona de inventário." />
        ) : (
          <table className="w-full border-collapse rounded-lg border border-gray-200 bg-white text-sm" aria-label="Lista de zonas">
            <thead>
              <tr className="border-b border-gray-200 bg-gray-50">
                <th scope="col" className="px-4 py-3 text-left font-semibold text-gray-700">ID</th>
                <th scope="col" className="px-4 py-3 text-left font-semibold text-gray-700">Nome</th>
                <th scope="col" className="px-4 py-3 text-left font-semibold text-gray-700">Site ID</th>
                <th scope="col" className="px-4 py-3 text-left font-semibold text-gray-700">Dimensões</th>
                <th scope="col" className="px-4 py-3 text-left font-semibold text-gray-700">Tipo</th>
                <th scope="col" className="px-4 py-3 text-left font-semibold text-gray-700">Ativo</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              {data?.map((z) => (
                <tr key={z.id} className="hover:bg-gray-50">
                  <td className="px-4 py-3 font-mono text-xs text-gray-500">{z.id}</td>
                  <td className="px-4 py-3 font-medium text-gray-900">{z.name}</td>
                  <td className="px-4 py-3 text-gray-600">{z.siteId}</td>
                  <td className="px-4 py-3 text-gray-600">{z.width}×{z.height}</td>
                  <td className="px-4 py-3 text-gray-600">{z.type}</td>
                  <td className="px-4 py-3">
                    <span className={`inline-flex rounded-full px-2 py-0.5 text-xs font-medium ${z.active ? "bg-green-100 text-green-800" : "bg-gray-100 text-gray-600"}`}>
                      {z.active ? "Ativo" : "Inativo"}
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
