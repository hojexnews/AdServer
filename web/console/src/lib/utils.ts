/**
 * cn() — utilitário de composição de classNames (alicerce shadcn/ui, G0/frontend E10).
 *
 * `clsx` resolve classes condicionais; `tailwind-merge` resolve conflitos de
 * utilitários Tailwind (ex.: "px-2 px-4" → "px-4"), essencial para permitir
 * que consumidores sobrescrevam classes via prop `className` sem CSS
 * duplicado/inconsistente — o padrão que shadcn/ui usa em todos os
 * componentes gerados sobre Base UI/React Aria (mandato §2.5).
 *
 * Este arquivo é o único ponto de entrada do alicerce shadcn/ui neste
 * addon: NÃO reescreve os componentes já gate-verdes (status-badge,
 * empty-state, money-display, payment-status-badge) — apenas dá a eles (e a
 * futuros componentes) um consumidor real e padronizado para composição de
 * classes.
 */

import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs));
}
