# media-name-parser
A service to parse filenames to pinpoint media content.

## Authentication

Two independent credentials, for two audiences.

**API keys** authenticate machine callers on `/api/v1/*`, as
`Authorization: Bearer mnp_…`. Mint one with
`npm run seed:key -- <email> [label]`; the secret prints once, to stdout, and
is not recoverable. The key belongs to a real user row.

**Sessions** authenticate people, via a magic link at `/sign-in`. GitHub
appears as an option only when `GITHUB_CLIENT_ID` and `GITHUB_CLIENT_SECRET`
are both set. `/admin` requires the `admin` role: grant it with
`npm run admin:promote -- <email>`, remove it with `--revoke`.

The two do not mix. An API key cannot reach a session-guarded route
(`/admin`, and `/api/v1/admin/whoami`, the one session-authenticated route
under the otherwise key-authenticated `/api/v1/*` prefix), and a session
cannot reach a key-guarded one.

`/api/auth/admin/*` -- the fifteen user-administration endpoints the `admin`
plugin would otherwise publish (create/ban/impersonate/set-role and the
rest) -- is deliberately not served. The plugin is registered only for the
`role` column and schema it contributes; its own endpoints bypass
`lib/auth/roles.ts` (`set-role` writes the column directly) and include
`impersonate-user`, which mints a session for any user. Our catch-all
refuses that prefix with a 404 before either endpoint is ever reached.

### Environment

| Variable | Required | Purpose |
|---|---|---|
| `DATABASE_URL` | yes | Neon connection string |
| `BETTER_AUTH_SECRET` | yes | signs sessions; Better Auth will not start without it |
| `BETTER_AUTH_URL` | production | the app's own origin, used to build magic-link URLs; optional in development, where it defaults to `http://localhost:3000` |
| `TMDB_READ_ACCESS_TOKEN` | yes | TMDB v4 read access token; the code also accepts the older `TMDB_API_KEY` name |
| `CRON_SECRET` | production | the bearer token Vercel Cron presents to the sweep route |
| `GITHUB_CLIENT_ID`, `GITHUB_CLIENT_SECRET` | no | enables GitHub sign-in when both are set |
| `MAGIC_LINK_SINK` | no | `1` collects magic links in memory instead of sending mail |

No mailer is configured. With `MAGIC_LINK_SINK` unset, requesting a magic link
logs that nothing was delivered rather than pretending to send it. Choosing a
mail provider is deliberately out of this plan's scope.
