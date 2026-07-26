import Link from "next/link";

export default function HomePage() {
  return (
    <div>
      <h1 className="text-2xl font-bold text-foreground">
        Console self-service
      </h1>
      <p className="mt-2 text-muted-foreground">
        Gerencie anunciantes, campanhas, banners, zonas e regras de entrega.
      </p>
      <nav aria-label="Atalhos rápidos" className="mt-8 grid grid-cols-2 gap-4 sm:grid-cols-3">
        {[
          { href: "/advertisers", label: "Anunciantes", desc: "Gerenciar anunciantes e contas" },
          { href: "/campaigns", label: "Campanhas", desc: "CPM, CPC, CPA, Tenancy" },
          { href: "/banners", label: "Banners", desc: "Criativos: image, html5, video" },
          { href: "/sites", label: "Sites", desc: "Sites e propriedades" },
          { href: "/zones", label: "Zonas", desc: "Zonas de inventário e vínculos N:N" },
          { href: "/dashboard", label: "Dashboard", desc: "KPIs consolidados e ao vivo" },
        ].map((item) => (
          <Link
            key={item.href}
            href={item.href}
            className="rounded-lg border border-border bg-card p-4 shadow-sm hover:border-brand-500 hover:shadow-md focus-visible:ring-2 focus-visible:ring-brand-500"
          >
            <div className="font-semibold text-foreground">{item.label}</div>
            <div className="mt-1 text-sm text-muted-foreground">{item.desc}</div>
          </Link>
        ))}
      </nav>
    </div>
  );
}
