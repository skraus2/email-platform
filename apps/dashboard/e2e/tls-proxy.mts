/**
 * HTTPS in front of the dashboard for the WebKit tests: WebKit drops Secure
 * cookies over plain http, also on 127.0.0.1, so the production session
 * cookie needs a TLS connection. Forwards every request unchanged to the
 * dashboard, with X-Forwarded-Proto: https. Started by Playwright; the
 * certificate is a throwaway one made by scripts/e2e.sh.
 */
import { readFileSync } from "node:fs";
import http from "node:http";
import https from "node:https";

const env = (name: string): string => {
  const v = process.env[name];
  if (!v) {
    throw new Error(`${name} is not set; run the suite with \`make test-e2e\``);
  }
  return v;
};

const target = Number(env("E2E_DASHBOARD_PORT"));
const port = Number(env("E2E_TLS_PORT"));

https
  .createServer(
    {
      cert: readFileSync(env("E2E_TLS_CERT")),
      key: readFileSync(env("E2E_TLS_KEY")),
    },
    (req, res) => {
      const upstream = http.request(
        {
          host: "127.0.0.1",
          port: target,
          method: req.method,
          path: req.url,
          headers: { ...req.headers, "x-forwarded-proto": "https" },
        },
        (up) => {
          res.writeHead(up.statusCode ?? 502, up.headers);
          up.pipe(res);
        },
      );
      // Until the dashboard is up, Playwright's readiness check sees 502.
      upstream.on("error", () => {
        if (!res.headersSent) {
          res.writeHead(502);
        }
        res.end();
      });
      req.pipe(upstream);
    },
  )
  .listen(port, "127.0.0.1");
