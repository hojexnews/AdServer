"use client";

import { useState } from "react";
import { useForm, useWatch } from "react-hook-form";
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

  const { register, handleSubmit, control, formState: { errors }, reset } =
    useForm<CreateValues>({
      resolver: zodResolver(CreateSchema),
      defaultValues: { creativeType: "image", width: 300, height: 250 },
    });

  // useWatch (não watch): compatível com o React Compiler do Next 16 (watch()
  // não é memoizável → warning react-hooks/incompatible-library) + re-render escopado.
  const creativeType = useWatch({ control, name: "creativeType" });

  if (isLoading) return <LoadingState label="Carregando banners..." />;
  if (error) return <ErrorState message={error.message} retry={() => { void refetch(); }} />;

  return (
    <div>
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold text-foreground">Banners / Criativos</h1>
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
          className="mt-4 space-y-4 rounded-lg border border-border bg-card p-6"
        >
          <h2 className="font-semibold text-foreground">Novo banner</h2>
          <div className="grid grid-cols-2 gap-4">
            <div>
              <label htmlFor="campaignId" className="block text-sm font-medium text-foreground">ID da Campanha</label>
              <input id="campaignId" type="text" inputMode="numeric" {...register("campaignId")} className="mt-1 w-full rounded-md border border-border px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500" />
              {errors.campaignId && <p role="alert" className="mt-1 text-xs text-red-600 dark:text-red-300">{errors.campaignId.message}</p>}
            </div>
            <div>
              <label htmlFor="bannerName" className="block text-sm font-medium text-foreground">Nome</label>
              <input id="bannerName" type="text" {...register("name")} className="mt-1 w-full rounded-md border border-border px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500" />
              {errors.name && <p role="alert" className="mt-1 text-xs text-red-600 dark:text-red-300">{errors.name.message}</p>}
            </div>
          </div>

          <div className="grid grid-cols-3 gap-4">
            <div>
              <label htmlFor="creativeType" className="block text-sm font-medium text-foreground">Tipo de criativo</label>
              <select id="creativeType" {...register("creativeType")} className="mt-1 w-full rounded-md border border-border px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500">
                <option value="image">Imagem</option>
                <option value="html5">HTML5</option>
                <option value="thirdparty_tag">Tag terceiro</option>
                <option value="video">Vídeo</option>
              </select>
            </div>
            <div>
              <label htmlFor="bWidth" className="block text-sm font-medium text-foreground">Largura (px)</label>
              <input id="bWidth" type="number" min={1} {...register("width")} className="mt-1 w-full rounded-md border border-border px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500" />
            </div>
            <div>
              <label htmlFor="bHeight" className="block text-sm font-medium text-foreground">Altura (px)</label>
              <input id="bHeight" type="number" min={1} {...register("height")} className="mt-1 w-full rounded-md border border-border px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500" />
            </div>
          </div>

          <div>
            <label htmlFor="assetUrl" className="block text-sm font-medium text-foreground">URL do asset (CDN)</label>
            <input id="assetUrl" type="url" placeholder="https://cdn.example.com/banner.jpg" {...register("assetUrl")} className="mt-1 w-full rounded-md border border-border px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500" />
            {errors.assetUrl && <p role="alert" className="mt-1 text-xs text-red-600 dark:text-red-300">{errors.assetUrl.message}</p>}
          </div>

          {creativeType !== "thirdparty_tag" && (
            <div>
              <label htmlFor="destUrl" className="block text-sm font-medium text-foreground">URL de destino (landing page)</label>
              <input id="destUrl" type="url" placeholder="https://exemplo.com.br" {...register("destUrl")} className="mt-1 w-full rounded-md border border-border px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500" />
              {errors.destUrl && <p role="alert" className="mt-1 text-xs text-red-600 dark:text-red-300">{errors.destUrl.message}</p>}
            </div>
          )}

          {create.error && <ErrorState message={create.error.message} />}

          <div className="flex gap-3">
            <button type="submit" disabled={create.isPending} aria-busy={create.isPending} className="rounded-md bg-brand-600 px-4 py-2 text-sm font-semibold text-white hover:bg-brand-700 focus-visible:ring-2 focus-visible:ring-brand-500 disabled:opacity-50">
              {create.isPending ? "Salvando..." : "Criar"}
            </button>
            <button type="button" onClick={() => { setShowForm(false); reset(); }} className="rounded-md border border-border px-4 py-2 text-sm font-medium text-foreground hover:bg-muted">
              Cancelar
            </button>
          </div>
        </form>
      )}

      <div className="mt-6">
        {data?.length === 0 ? (
          <EmptyState title="Nenhum banner cadastrado" />
        ) : (
          <table className="w-full border-collapse rounded-lg border border-border bg-card text-sm" aria-label="Lista de banners">
            <thead>
              <tr className="border-b border-border bg-muted">
                <th scope="col" className="px-4 py-3 text-left font-semibold text-foreground">ID</th>
                <th scope="col" className="px-4 py-3 text-left font-semibold text-foreground">Nome</th>
                <th scope="col" className="px-4 py-3 text-left font-semibold text-foreground">Campanha</th>
                <th scope="col" className="px-4 py-3 text-left font-semibold text-foreground">Tipo</th>
                <th scope="col" className="px-4 py-3 text-left font-semibold text-foreground">Dimensões</th>
                <th scope="col" className="px-4 py-3 text-left font-semibold text-foreground">Ativo</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {data?.map((b) => (
                <tr key={b.id} className="hover:bg-muted">
                  <td className="px-4 py-3 font-mono text-xs text-muted-foreground">{b.id}</td>
                  <td className="px-4 py-3 font-medium text-foreground">{b.name}</td>
                  <td className="px-4 py-3 text-muted-foreground">{b.campaignId}</td>
                  <td className="px-4 py-3 text-muted-foreground">{b.creativeType}</td>
                  <td className="px-4 py-3 text-muted-foreground">{b.width}×{b.height}</td>
                  <td className="px-4 py-3">
                    <span className={`inline-flex rounded-full px-2 py-0.5 text-xs font-medium ${b.active ? "bg-green-100 text-green-800 dark:bg-green-500/15 dark:text-green-200" : "bg-muted text-muted-foreground"}`}>
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
