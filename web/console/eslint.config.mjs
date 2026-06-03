import { dirname } from "path";
import { fileURLToPath } from "url";
import { FlatCompat } from "@eslint/eslintrc";

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);

const compat = new FlatCompat({
  baseDirectory: __dirname,
});

const eslintConfig = [
  ...compat.extends("next/core-web-vitals"),
  {
    rules: {
      // Money safety: proíbe uso de Number() em contextos monetários
      // (enforced via convention + code review; não há regra semântica automática)
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
];

export default eslintConfig;
