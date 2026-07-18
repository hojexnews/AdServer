// ESLint flat config — Next.js 16 nativo (G0/frontend E10, alinhamento §2.5).
// O Next 16 removeu `next lint`; o gate agora é `eslint` direto (npm run lint).
// eslint-config-next 16 publica configs FLAT (`./core-web-vitals`) — usá-las via
// FlatCompat.extends quebra ("circular structure"), então importamos a flat nativa.
import { defineConfig, globalIgnores } from "eslint/config";
import nextVitals from "eslint-config-next/core-web-vitals";

const eslintConfig = defineConfig([
  ...nextVitals,
  {
    rules: {
      // Money safety: proíbe parseFloat em qualquer contexto (TX-2/DA-10 — dinheiro
      // é sempre string DECIMAL via decimal.js, nunca float no cliente).
      "no-restricted-syntax": [
        "error",
        {
          selector: "CallExpression[callee.name='parseFloat']",
          message:
            "parseFloat é proibido. Use decimal.js para valores monetários (TX-2/DA-10).",
        },
      ],
    },
  },
  // Substitui os ignores default do eslint-config-next (a flat config não os aplica sozinha).
  globalIgnores([".next/**", "out/**", "build/**", "next-env.d.ts"]),
]);

export default eslintConfig;
