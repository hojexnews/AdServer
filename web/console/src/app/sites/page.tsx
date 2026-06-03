"use client";

import { useState } from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { trpc } from "@/lib/trpc";
import { LoadingState, ErrorState, EmptyState } from "@/components/ui/empty-state";

const CreateSchema = z.object({
  name: z.string().min(1, "Nome obrigatório"),
  url: z.string().url("URL inválida"),
});
type CreateValues = z.infer<typeof CreateSchema>;

export default function SitesPage() {
  const [showForm, setShowForm] = useState(false);
  const utils = trpc.useUtils();
  const { data, isLoading, error, refetch } = trpc.cfg.site.list.useQuery();
  const create = trpc.cfg.site.create.useMutation({
    onSuccess: () => {
      void utils.cfg.site.list.invalidate();
      setShowForm(false);
    },
  });
  const { register, handleSubmit, formState: { errors }, reset } =
    useForm<CreateValues>({ resolver: zodResolver(CreateSchema) });

  if (isLoading) return <LoadingState label="Carregando sites..." />;
  if (error) return <ErrorState message={error.message} retry={() => { void refetch(); }} />;

  return (
    <div>
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold text-gray-900">Sites</h1>
        <button onClick={() => setShowForm((v) => !v)} className="rounded-md bg-brand-600 px-4 py-2 text-sm font-semibold text-white hover:bg-brand-700 focus-visible:ring-2 focus-visible:ring-brand-500">
          {showForm ? "Cancelar" : "Novo site"}
        </button>
      </div>
      {showForm && (
        <form onSubmit={handleSubmit((v) => void create.mutate(v))} aria-label="Criar site" noValidate className="mt-4 space-y-4 rounded-lg border border-gray-200 bg-white p-6">
          <h2 className="font-semibold text-gray-800">Novo site</h2>
          <div>
            <label htmlFor="siteName" className="block text-sm font-medium text-gray-700">Nome</label>
            <input id="siteName" type="text" {...register("name")} className="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500" />
            {errors.name && <p role="alert" className="mt-1 text-xs text-red-600">{errors.name.message}</p>}
          </div>
          <div>
            <label htmlFor="siteUrl" className="block text-sm font-medium text-gray-700">URL</label>
            <input id="siteUrl" type="url" placeholder="https://exemplo.com.br" {...register("url")} className="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500" />
            {errors.url && <p role="alert" className="mt-1 text-xs text-red-600">{errors.url.message}</p>}
          </div>
          {create.error && <ErrorState message={create.error.message} />}
          <div className="flex gap-3">
            <button type="submit" disabled={create.isPending} aria-busy={create.isPending} className="rounded-md bg-brand-600 px-4 py-2 text-sm font-semibold text-white hover:bg-brand-700 disabled:opacity-50 focus-visible:ring-2 focus-visible:ring-brand-500">
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
          <EmptyState title="Nenhum site cadastrado" />
        ) : (
          <table className="w-full border-collapse rounded-lg border border-gray-200 bg-white text-sm" aria-label="Lista de sites">
            <thead>
              <tr className="border-b border-gray-200 bg-gray-50">
                <th scope="col" className="px-4 py-3 text-left font-semibold text-gray-700">ID</th>
                <th scope="col" className="px-4 py-3 text-left font-semibold text-gray-700">Nome</th>
                <th scope="col" className="px-4 py-3 text-left font-semibold text-gray-700">URL</th>
                <th scope="col" className="px-4 py-3 text-left font-semibold text-gray-700">Ativo</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              {data?.map((s) => (
                <tr key={s.id} className="hover:bg-gray-50">
                  <td className="px-4 py-3 font-mono text-xs text-gray-500">{s.id}</td>
                  <td className="px-4 py-3 font-medium text-gray-900">{s.name}</td>
                  <td className="px-4 py-3 text-blue-600 underline"><a href={s.url} target="_blank" rel="noopener noreferrer">{s.url}</a></td>
                  <td className="px-4 py-3">
                    <span className={`inline-flex rounded-full px-2 py-0.5 text-xs font-medium ${s.active ? "bg-green-100 text-green-800" : "bg-gray-100 text-gray-600"}`}>
                      {s.active ? "Ativo" : "Inativo"}
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
