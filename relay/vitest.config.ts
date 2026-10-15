import { cloudflareTest } from "@cloudflare/vitest-plugin";
import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [
    cloudflareTest({
      wrangler: { configPath: "./wrangler.toml" },
      miniflare: {
        // Test-only values; the APNs key itself is generated in the tests.
        bindings: { RELAY_SECRET: "test-relay-secret", APNS_KEY_ID: "TESTKEY123", APNS_TEAM_ID: "TESTTEAM12" },
      },
    }),
  ],
});
