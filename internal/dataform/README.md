# BigQuery Studio saved queries on the Dataform API

This was checked live against the console in November 2026. See `live_test.go`.

| Console concept | Dataform v1 |
|---|---|
| Saved query | A repository with the label `single-file-asset-type: sql`, named by a UUID |
| Title | `displayName`. Titles need not be unique. |
| Region "US" (multi-region) | Location `us-central1`. The location `us` does not exist and returns 403. |
| Query text | The file `content.sql` at the head of the default branch (`repositories.readFile`) |
| Each save | One commit (`repositories.commit`). The console's first commit message is "Created". |
| History | `repositories.fetchHistory`, newest first |
| Workspaces | None |
| Private or shared | Repository IAM. A private query grants `roles/dataform.admin` to its creator only. |
| Last modified | `internalMetadata.last_modified_time` (a JSON string) |

**Writes**
- A save is `commit` with `requiredHeadCommitSha` set to the head the text was read at.
- If someone saved in the console since, Dataform rejects the commit with HTTP 400 `failedPrecondition` and the message "Required HEAD commit SHA … does not match actual HEAD commit SHA …". Nothing is overwritten, and bqui reports `ErrConflict`.
- Create is `repositories.create` with `setAuthenticatedUserAdmin: true` (a private query), then a first commit.
- Rename patches `displayName`.
- Delete uses `force: true`.

Commits are signed with the account email from Google's tokeninfo endpoint. If the token has no email scope, bqui uses a neutral `bqui` author.
