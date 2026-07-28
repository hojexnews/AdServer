"use client";

import { useState } from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { trpc } from "@/lib/trpc";
import { LoadingState, ErrorState, EmptyState } from "@/components/ui/empty-state";

const CreateSchema = z.object({
  name: z.string().min(1, "Nome obrigatório"),
  loginUsername: z.string().min(1, "Username obrigatório"),
  loginPassword: z.string().min(8, "Mínimo 8 caracteres"),
  isNetwork: z.boolean(),
});
type CreateValues = z.infer<typeof CreateSchema>;

export default function AdvertisersPage() {
  const [showForm, setShowForm] = useState(false);
  const utils = trpc.useUtils();

  const { data, isLoading, error, refetch } =
    trpc.cfg.advertiser.list.useQuery();

  const create = trpc.cfg.advertiser.create.useMutation({
    onSuccess: () => {
      void utils.cfg.advertiser.list.invalidate();
      setShowForm(false);
    },
  });

  const del = trpc.cfg.advertiser.delete.useMutation({
    onSuccess: () => {
      void utils.cfg.advertiser.list.invalidate();
    },
  });

  const {
    register,
    handleSubmit,
    formState: { errors },
    reset,
  } = useForm<CreateValues>({
    resolver: zodResolver(CreateSchema),
    defaultValues: { isNetwork: false },
  });

  if (isLoading) return <LoadingState label="Carregando anunciantes..." />;
  if (error)
    return <ErrorState message={error.message} retry={() => { void refetch(); }} />;

  return (
    <div>
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold text-foreground">Anunciantes</h1>
        <button
          onClick={() => setShowForm((v) => !v)}
          className="rounded-md bg-brand-600 px-4 py-2 text-sm font-semibold text-white hover:bg-brand-700 focus-visible:ring-2 focus-visible:ring-brand-500"
        >
          {showForm ? "Cancelar" : "Novo anunciante"}
        </button>
      </div>

      {showForm && (
        <form
          onSubmit={handleSubmit((v) => void create.mutate(v))}
          aria-label="Criar anunciante"
          noValidate
          className="mt-4 space-y-4 rounded-lg border border-border bg-card p-6"
        >
          <h2 className="font-semibold text-foreground">Novo anunciante</h2>

          <div>
            <label htmlFor="name" className="block text-sm font-medium text-foreground">
              Nome
            </label>
            <input
              id="name"
              type="text"
              {...register("name")}
              aria-describedby={errors.name ? "name-err" : undefined}
              className="mt-1 w-full rounded-md border border-border px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500"
            />
            {errors.name && (
              <p id="name-err" role="alert" className="mt-1 text-xs text-red-600 dark:text-red-300">
                {errors.name.message}
              </p>
            )}
          </div>

          <div>
            <label htmlFor="loginUsername" className="block text-sm font-medium text-foreground">
              Username (login)
            </label>
            <input
              id="loginUsername"
              type="text"
              autoComplete="off"
              {...register("loginUsername")}
              aria-describedby={errors.loginUsername ? "username-err" : undefined}
              className="mt-1 w-full rounded-md border border-border px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500"
            />
            {errors.loginUsername && (
              <p id="username-err" role="alert" className="mt-1 text-xs text-red-600 dark:text-red-300">
                {errors.loginUsername.message}
              </p>
            )}
          </div>

          <div>
            <label htmlFor="loginPassword" className="block text-sm font-medium text-foreground">
              Senha
            </label>
            <input
              id="loginPassword"
              type="password"
              autoComplete="new-password"
              {...register("loginPassword")}
              aria-describedby={errors.loginPassword ? "pass-err" : undefined}
              className="mt-1 w-full rounded-md border border-border px-3 py-2 text-sm focus-visible:ring-2 focus-visible:ring-brand-500"
            />
            {errors.loginPassword && (
              <p id="pass-err" role="alert" className="mt-1 text-xs text-red-600 dark:text-red-300">
                {errors.loginPassword.message}
              </p>
            )}
          </div>

          <div className="flex items-center gap-2">
            <input
              id="isNetwork"
              type="checkbox"
              {...register("isNetwork")}
              className="h-4 w-4 rounded border-border text-brand-600 focus-visible:ring-2 focus-visible:ring-brand-500"
            />
            <label htmlFor="isNetwork" className="text-sm text-foreground">
              Ad network (não cliente direto)
            </label>
          </div>

          {create.error && (
            <ErrorState message={create.error.message} />
          )}

          <div className="flex gap-3">
            <button
              type="submit"
              disabled={create.isPending}
              aria-busy={create.isPending}
              className="rounded-md bg-brand-600 px-4 py-2 text-sm font-semibold text-white hover:bg-brand-700 focus-visible:ring-2 focus-visible:ring-brand-500 disabled:opacity-50"
            >
              {create.isPending ? "Salvando..." : "Criar"}
            </button>
            <button
              type="button"
              onClick={() => { setShowForm(false); reset(); }}
              className="rounded-md border border-border px-4 py-2 text-sm font-medium text-foreground hover:bg-muted focus-visible:ring-2 focus-visible:ring-brand-500"
            >
              Cancelar
            </button>
          </div>
        </form>
      )}

      <div className="mt-6">
        {data?.length === 0 ? (
          <EmptyState
            title="Nenhum anunciante cadastrado"
            description="Crie o primeiro anunciante para começar."
          />
        ) : (
          <table
            className="w-full border-collapse rounded-lg border border-border bg-card text-sm"
            aria-label="Lista de anunciantes"
          >
            <thead>
              <tr className="border-b border-border bg-muted">
                <th scope="col" className="px-4 py-3 text-left font-semibold text-foreground">ID</th>
                <th scope="col" className="px-4 py-3 text-left font-semibold text-foreground">Nome</th>
                <th scope="col" className="px-4 py-3 text-left font-semibold text-foreground">Username</th>
                <th scope="col" className="px-4 py-3 text-left font-semibold text-foreground">Network</th>
                <th scope="col" className="px-4 py-3 text-left font-semibold text-foreground">Ativo</th>
                <th scope="col" className="px-4 py-3 text-left font-semibold text-foreground">
                  <span className="sr-only">Ações</span>
                </th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {data?.map((adv) => (
                <tr key={adv.id} className="hover:bg-muted">
                  <td className="px-4 py-3 font-mono text-xs text-muted-foreground">{adv.id}</td>
                  <td className="px-4 py-3 font-medium text-foreground">{adv.name}</td>
                  <td className="px-4 py-3 text-muted-foreground">{adv.loginUsername}</td>
                  <td className="px-4 py-3">
                    <span aria-label={adv.isNetwork ? "Sim" : "Não"}>
                      {adv.isNetwork ? "Sim" : "Não"}
                    </span>
                  </td>
                  <td className="px-4 py-3">
                    <span
                      className={`inline-flex rounded-full px-2 py-0.5 text-xs font-medium ${
                        adv.active
                          ? "bg-green-100 text-green-800 dark:bg-green-500/15 dark:text-green-200"
                          : "bg-muted text-muted-foreground"
                      }`}
                    >
                      {adv.active ? "Ativo" : "Inativo"}
                    </span>
                  </td>
                  <td className="px-4 py-3 text-right">
                    <button
                      onClick={() => void del.mutate({ id: adv.id })}
                      disabled={del.isPending}
                      aria-label={`Excluir anunciante ${adv.name}`}
                      className="text-xs text-red-600 dark:text-red-300 hover:text-red-800 dark:hover:text-red-200 focus-visible:ring-2 focus-visible:ring-red-500 disabled:opacity-50"
                    >
                      Excluir
                    </button>
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
