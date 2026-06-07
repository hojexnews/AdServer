/**
 * Logo — marca Hojex AdServer (o "H" cromado sobre os 4 quadrantes).
 * Usa next/image com import estático (dimensões intrínsecas + otimização).
 * White-label: `mark` e `wordmark` são sobrescrevíveis por tenant.
 * WCAG 2.2 AA: quando o wordmark textual está visível, a imagem é decorativa
 * (alt="" + aria-hidden) para o leitor de tela não anunciar a marca duas vezes.
 */

import Image, { type StaticImageData } from "next/image";
import logoMark from "@/assets/logo-mark.png";

interface LogoProps {
  /** Altura do símbolo em px (largura mantém o aspecto). Default: 32. */
  markSize?: number;
  /** Exibe o wordmark textual ao lado do símbolo. Default: true. */
  showWordmark?: boolean;
  /** Texto do wordmark. Default: "AdServer Console". */
  wordmark?: string;
  /** Override do símbolo (white-label por tenant). */
  mark?: StaticImageData;
  /** Classes extras no contêiner. */
  className?: string;
}

export function Logo({
  markSize = 32,
  showWordmark = true,
  wordmark = "AdServer Console",
  mark = logoMark,
  className,
}: LogoProps) {
  // Largura derivada do aspecto intrínseco — evita distorção e o warning do next/image.
  const markWidth = Math.round(markSize * (mark.width / mark.height));

  return (
    <span className={["flex items-center gap-2", className].filter(Boolean).join(" ")}>
      <Image
        src={mark}
        alt={showWordmark ? "" : "Hojex AdServer"}
        aria-hidden={showWordmark || undefined}
        width={markWidth}
        height={markSize}
        priority
      />
      {showWordmark && (
        <span className="whitespace-nowrap text-base font-semibold text-brand-700">
          {wordmark}
        </span>
      )}
    </span>
  );
}
