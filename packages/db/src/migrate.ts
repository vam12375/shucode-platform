import { readFile, readdir } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import postgres from "postgres";

const MIGRATIONS_DIR = join(dirname(fileURLToPath(import.meta.url)), "../migrations");

export interface RunMigrationsOptions {
  log?: (message: string) => void;
}

/** 顺序执行 migrations/*.sql，以 platform_migrations 记录已应用版本；每个文件一个事务。 */
export async function runMigrations(
  databaseUrl: string,
  options: RunMigrationsOptions = {},
): Promise<void> {
  const sql = postgres(databaseUrl, { max: 1, onnotice: () => {} });
  try {
    await sql`
      CREATE TABLE IF NOT EXISTS platform_migrations (
        name text PRIMARY KEY,
        applied_at timestamptz NOT NULL DEFAULT now()
      )
    `;
    const files = (await readdir(MIGRATIONS_DIR))
      .filter((name) => name.endsWith(".sql"))
      .sort((a, b) => a.localeCompare(b));
    for (const file of files) {
      const existing = await sql`SELECT name FROM platform_migrations WHERE name = ${file}`;
      if (existing.length > 0) {
        continue;
      }
      const content = await readFile(join(MIGRATIONS_DIR, file), "utf8");
      await sql.begin(async (tx) => {
        await tx.unsafe(content);
        await tx`INSERT INTO platform_migrations (name) VALUES (${file})`;
      });
      options.log?.(`applied migration: ${file}`);
    }
  } finally {
    await sql.end({ timeout: 5 });
  }
}
