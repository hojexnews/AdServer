"use client";

import { useState } from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { trpc } from "@/lib/trpc";
import { LoadingState, ErrorState, EmptyState } from "@/components/ui/empty-state";

const CreateSchema = z
  .object({
    campaignId: z.string().min(1, "ID da campanha obrigatório"),
    name: z.string().min(1, "Nome obrigatório"),
    creativeType: z.enum(["image", "html5", "thirdparty_tag", "video"]),
    assetUrl: z.string().url("URL inválida"),
    destUrl: z.string().url("URL inválida").nullable().optional(),
    width: z.coerce.number().int().positive(),
    height: z.coerce.number().int().positive(),
  })
  .refine(
    (d) => {
      if (d.creativeType !== "thirdparty_tag") return d.destUrl != null;
      return true;
    },
    { message: "destUrl obrigatório para image, html5, video", path: ["destUrl"] }
  );

type CreateValues = z.infer<typeof CreateSchema>;

export default function BannersPage() {
  const [showForm, setShowForm] = useState(false);
  const utils = trpc.useUtils();

  const { data, isLoading, error, refetch } =
    trpc.cfg.banner.list.useQuery({});

  const create = trpc.cfg.banner.create.useMutation({
    onSuccess: () => {
      void utils.cfg.banner.list.invalidate();
      setShowForm(false);
    },
  });

  const { register, handleSubmit, watch, formState: { errors }, reset } =
    useForm<CreateValues>({
      resolver: zodResolver(CreateSchema),
      defaultValues: { creativeType: "image", width: 300, height: 250 },
    });

  const creativeType = watch("creativeType");

  if (isLoading) return <LoadingState label="Carregando banners..." />;
  if (error) return <ErrorState message={error.message} retry={() => { void refetch(); }} />;

  return (
    <div>
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold text-gray-900">Banners / Criativos</h1>
        <button
          onClick={() => setShowForm((v) => !v)}
          className="rounded-md bg-brand-600 px-4 py-2 text-sm font-semibold text-white hover:bg-brand-700 focus-visible:ring-2 focus-visible:ring-brand-500"
        >
          {showForm ? "Cancelar" : "Novo banner"}
        </button>
      </div>

      {showForm && (
        <form
          onSubmit={handleSubmit((v) =>
            void create.mutate({
              ...v,
              destUrl: v.destUrl ?? null,
            })
          )}
          aria-label="Criar banner"
          noValidate
          className="mt-4 space-y-4 rounded-lg border border-gray-200 bg-white p-6"
        >
          <h2 className="font-semibold text-gray-800">Novo banner</h2>
          <div className="grid grid-cols-2 gap-4">
            <div>
              <label htmlFor="campaignId" className="block text-sm font-medium text-gray-700">ID da Campanha</label>
              <input id="campaignId" type="text" inputMode="numeric" {...register("campaignId")} className="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500" />
              {errors.campaignId && <p role="alert" className="mt-1 text-xs text-red-600">{errors.campaignId.message}</p>}
            </div>
            <div>
              <label htmlFor="bannerName" className="block text-sm font-medium text-gray-700">Nome</label>
              <input id="bannerName" type="text" {...register("name")} className="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500" />
              {errors.name && <p role="alert" className="mt-1 text-xs text-red-600">{errors.name.message}</p>}
            </div>
          </div>

          <div className="grid grid-cols-3 gap-4">
            <div>
              <label htmlFor="creativeType" className="block text-sm font-medium text-gray-700">Tipo de criativo</label>
              <select id="creativeType" {...register("creativeType")} className="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500">
                <option value="image">Imagem</option>
                <option value="html5">HTML5</option>
                <option value="thirdparty_tag">Tag terceiro</option>
                <option value="video">Vídeo</option>
              </select>
            </div>
            <div>
              <label htmlFor="bWidth" className="block text-sm font-medium text-gray-700">Largura (px)</label>
              <input id="bWidth" type="number" min={1} {...register("width")} className="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500" />
            </div>
            <div>
              <label htmlFor="bHeight" className="block text-sm font-medium text-gray-700">Altura (px)</label>
              <input id="bHeight" type="number" min={1} {...register("height")} className="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500" />
            </div>
          </div>

          <div>
            <label htmlFor="assetUrl" className="block text-sm font-medium text-gray-700">URL do asset (CDN)</label>
            <input id="assetUrl" type="url" placeholder="https://cdn.example.com/banner.jpg" {...register("assetUrl")} className="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500" />
            {errors.assetUrl && <p role="alert" className="mt-1 text-xs text-red-600">{errors.assetUrl.message}</p>}
          </div>

          {creativeType !== "thirdparty_tag" && (
            <div>
              <label htmlFor="destUrl" className="block text-sm font-medium text-gray-700">URL de destino (landing page)</label>
              <input id="destUrl" type="url" placeholder="https://exemplo.com.br" {...register("destUrl")} className="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500" />
              {errors.destUrl && <p role="alert" className="mt-1 text-xs text-red-600">{errors.destUrl.message}</p>}
            </div>
          )}

          {create.error && <ErrorState message={create.error.message} />}

          <div className="flex gap-3">
            <button type="submit" disabled={create.isPending} aria-busy={create.isPending} className="rounded-md bg-brand-600 px-4 py-2 text-sm font-semibold text-white hover:bg-brand-700 focus-visible:ring-2 focus-visible:ring-brand-500 disabled:opacity-50">
              {create.isPending ? "Salvando..." : "Criar"}
            </button>
            <button type="button" onClick={() => { setShowForm(false); reset(); }} className="rounded-md border border-gray-300 px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50">
              Cancelar
            </button>
          </div>
        </form>
      )}

      <div className="mt-6">
        {data?.length === 0 ? (
          <EmptyState title="Nenhum banner cadastrado" />
        ) : (
          <table className="w-full border-collapse rounded-lg border border-gray-200 bg-white text-sm" aria-label="Lista de banners">
            <thead>
              <tr className="border-b border-gray-200 bg-gray-50">
                <th scope="col" className="px-4 py-3 text-left font-semibold text-gray-700">ID</th>
                <th scope="col" className="px-4 py-3 text-left font-semibold text-gray-700">Nome</th>
                <th scope="col" className="px-4 py-3 text-left font-semibold text-gray-700">Campanha</th>
                <th scope="col" className="px-4 py-3 text-left font-semibold text-gray-700">Tipo</th>
                <th scope="col" className="px-4 py-3 text-left font-semibold text-gray-700">Dimensões</th>
                <th scope="col" className="px-4 py-3 text-left font-semibold text-gray-700">Ativo</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              {data?.map((b) => (
                <tr key={b.id} className="hover:bg-gray-50">
                  <td className="px-4 py-3 font-mono text-xs text-gray-500">{b.id}</td>
                  <td className="px-4 py-3 font-medium text-gray-900">{b.name}</td>
                  <td className="px-4 py-3 text-gray-600">{b.campaignId}</td>
                  <td className="px-4 py-3 text-gray-600">{b.creativeType}</td>
                  <td className="px-4 py-3 text-gray-600">{b.width}×{b.height}</td>
                  <td className="px-4 py-3">
                    <span className={`inline-flex rounded-full px-2 py-0.5 text-xs font-medium ${b.active ? "bg-green-100 text-green-800" : "bg-gray-100 text-gray-600"}`}>
                      {b.active ? "Ativo" : "Inativo"}
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
