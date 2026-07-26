/**
 * Inicialização do tRPC v11 — instância compartilhada.
 */

import { initTRPC, TRPCError } from "@trpc/server";
import type { TrpcContext } from "./context.js";

/**
 * errorFormatter — redação fail-closed de erros INTERNOS (TX-5 / DA-11).
 *
 * FIX (31ª onda, bff-trpc-error-shape-leak): o `create()` era chamado sem
 * errorFormatter. Quando um procedure lança algo que NÃO é TRPCError — por
 * exemplo um erro do driver `pg` — o tRPC embrulha em INTERNAL_SERVER_ERROR
 * mas PRESERVA a mensagem original e a envia ao browser. Na prática isso
 * publicava, para qualquer anunciante autenticado, texto do tipo
 * `relation "config.campaigns" does not exist`, nomes de constraint, e
 * fragmentos de valor citados pelo Postgres em violação de unicidade — ou
 * seja, topologia do schema e, potencialmente, dado de outro tenant embutido
 * na mensagem de erro.
 *
 * Regra: erro que NÓS levantamos deliberadamente (TRPCError) tem mensagem
 * escrita para o usuário e passa. Qualquer outro vira mensagem genérica com
 * um correlationId; o texto real fica só no log do servidor. Mesma disciplina
 * que routers/copilot.ts já aplicava a erros upstream (corrId + mensagem
 * neutra) — aqui ela passa a valer para TODO o router.
 */
const t = initTRPC.context<TrpcContext>().create({
  errorFormatter({ shape, error }) {
    // Discriminador: redigir SOMENTE erro interno EMBRULHADO.
    //
    // Duas condições, ambas necessárias:
    //   code === "INTERNAL_SERVER_ERROR" — qualquer outro código é uma
    //       classificação DELIBERADA (BAD_REQUEST, FORBIDDEN, NOT_FOUND,
    //       CONFLICT, PRECONDITION_FAILED...) cuja mensagem foi escrita para o
    //       usuário e precisa chegar até ele.
    //   cause !== undefined — o tRPC preenche `cause` ao EMBRULHAR algo que
    //       não era TRPCError (ex.: erro do driver pg), copiando a mensagem
    //       original. Um TRPCError que nós construímos com {code, message} não
    //       tem cause.
    //
    // Por que não bastava `cause === undefined` (a primeira tentativa desta
    // onda, corrigida pela revisão adversarial do próprio diff): erro de
    // validação Zod chega como BAD_REQUEST **com** `cause = ZodError`
    // (verificado empiricamente contra @trpc/server 11.0.0). Redigi-lo
    // destruía todo o feedback de validação por campo do formulário — o
    // usuário passava a ver "Erro interno" ao digitar uma data inválida.
    //
    // Por que não bastava filtrar só por código: routers/copilot.ts lança
    // TRPCError deliberado COM INTERNAL_SERVER_ERROR e mensagem própria (que
    // já carrega o corrId dele); filtrar só por código apagaria essa mensagem.
    //
    // Deliberado COM cause cai na redação — fail-closed, o lado seguro.
    const isWrappedInternal =
      error.code === "INTERNAL_SERVER_ERROR" && error.cause !== undefined;

    if (!isWrappedInternal) {
      return shape;
    }

    const correlationId = crypto.randomUUID();
    console.error("trpc.internal_error", {
      correlationId,
      code: error.code,
      message: error.message,
      cause: error.cause instanceof Error ? error.cause.message : undefined,
      stack: error.stack,
    });

    return {
      ...shape,
      message: `Erro interno. ID de correlação: ${correlationId}`,
      data: {
        ...shape.data,
        // Nunca devolver stack ao cliente, em nenhum NODE_ENV.
        stack: undefined,
        correlationId,
      },
    };
  },
});

export const router = t.router;
export const publicProcedure = t.procedure;

/**
 * Middleware que garante que o contexto de tenant está presente.
 * Todos os procedimentos de dados usam tenantProcedure.
 */
const ensureTenant = t.middleware(({ ctx, next }) => {
  if (!ctx.tenantId) {
    throw new TRPCError({
      code: "UNAUTHORIZED",
      message: "tenant_id ausente na sessão. Acesso negado.",
    });
  }
  return next({ ctx });
});

export const tenantProcedure = t.procedure.use(ensureTenant);
