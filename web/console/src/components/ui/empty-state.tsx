/**
 * EmptyState, LoadingState, ErrorState — tratamento consistente de estados.
 * WCAG 2.2 AA: role="status" para loading, role="alert" para erro.
 */

interface EmptyStateProps {
  title: string;
  description?: string;
  action?: React.ReactNode;
}

export function EmptyState({ title, description, action }: EmptyStateProps) {
  return (
    <div
      className="flex flex-col items-center justify-center rounded-lg border-2 border-dashed border-gray-300 p-12 text-center"
      role="status"
      aria-label={title}
    >
      <p className="text-base font-medium text-gray-900">{title}</p>
      {description && (
        <p className="mt-1 text-sm text-gray-500">{description}</p>
      )}
      {action && <div className="mt-4">{action}</div>}
    </div>
  );
}

export function LoadingState({ label = "Carregando..." }: { label?: string }) {
  return (
    <div
      className="flex items-center justify-center py-12"
      role="status"
      aria-label={label}
      aria-live="polite"
    >
      <div
        className="h-8 w-8 animate-spin rounded-full border-4 border-brand-200 border-t-brand-600"
        aria-hidden="true"
      />
      <span className="ml-3 text-sm text-gray-600">{label}</span>
    </div>
  );
}

interface ErrorStateProps {
  title?: string;
  message: string;
  retry?: () => void;
}

export function ErrorState({
  title = "Erro ao carregar",
  message,
  retry,
}: ErrorStateProps) {
  return (
    <div
      className="rounded-md border border-red-200 bg-red-50 p-4"
      role="alert"
      aria-live="assertive"
    >
      <p className="font-medium text-red-800">{title}</p>
      <p className="mt-1 text-sm text-red-700">{message}</p>
      {retry && (
        <button
          onClick={retry}
          className="mt-2 text-sm font-medium text-red-800 underline hover:text-red-900 focus-visible:ring-2 focus-visible:ring-red-500"
        >
          Tentar novamente
        </button>
      )}
    </div>
  );
}
