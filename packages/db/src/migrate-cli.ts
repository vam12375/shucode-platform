import { loadEnvFilesUpwards } from "./env-file.js";
import { runMigrations } from "./migrate.js";

loadEnvFilesUpwards(process.cwd());

const databaseUrl = process.env.DATABASE_URL?.trim();
if (!databaseUrl) {
  console.error("缺少 DATABASE_URL（参考 .env.example）");
  process.exit(1);
}

await runMigrations(databaseUrl, { log: (message) => console.log(message) });
console.log("migrations done");
