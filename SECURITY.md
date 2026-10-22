# Security

bqt runs with your Google Cloud credentials and can start BigQuery jobs that cost money, so security reports are welcome.

## Reporting a problem

Please do not open a public issue. Report it privately through GitHub: the repository's **Security** tab, then **Report a vulnerability**. You should get a reply within a week.

Useful details: the bqt version (`bqt version`), your OS, what you did, and what happened.

## What counts

- A way past the agent policy: the command line or the MCP server running a non-`SELECT` statement, going over `agent_max_bytes`, or skipping the dry-run.
- Credentials, tokens or query results written somewhere they should not be (logs, hand-off files, the catalog).
- A crafted config file, `.sql` file, catalog entry or query result that makes bqt run code or commands.

## What bqt does not protect against

- Anyone, person or agent, who can edit your config file can raise the limits. Read-only IAM on the Execution project is the real guarantee, as the README says.
- BigQuery's own billing and permissions are Google's to enforce.

## Supported versions

Only the latest release gets fixes.
