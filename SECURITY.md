# Security Policy

The `stratus-runtime` maintainers take the security of this project seriously.
Because this runtime orchestrates ephemeral CI runners and hardware-isolated
sandboxes on Kubernetes, we appreciate reports that help keep deployments safe.

## Supported Versions

| Version                        | Supported          |
|--------------------------------|--------------------|
| `main` (latest commit)         | :white_check_mark: |
| Latest tagged release          | :white_check_mark: |
| Older tagged releases          | :x:                |

Security fixes are applied to `main` and to the most recent tagged release.
Older releases do not receive backported patches — please upgrade.

## Reporting a Vulnerability

**Please do not open a public GitHub issue, pull request, or discussion for
security vulnerabilities.** Public disclosure before a fix is available puts
all users at risk.

Use one of the following private channels instead:

1. **Preferred — GitHub Private Vulnerability Reporting.** Open the repository's
   **Security** tab and choose **Report a vulnerability**. This keeps the report
   private and lets us collaborate with you directly on the advisory.
2. **Email.** Send details to **support@scheduler-systems.com** with the word
   **`SECURITY`** in the subject line so we can route it quickly.

Please include as much of the following as you can:

- A description of the vulnerability and its impact.
- The component or code path affected (e.g. JIT controller, sandbox controller,
  HMAC webhook proxy).
- Steps to reproduce or a proof of concept.
- Affected version(s), commit, or release tag.
- Any suggested remediation.

## Our Commitment

- We will **acknowledge** your report within **3 business days**.
- After acknowledgement we will **triage** the issue, confirm the impact, and
  share a **remediation timeline** once triage is complete.
- We will keep you informed of progress and credit you in the advisory when a
  fix is published, unless you prefer to remain anonymous.

Thank you for helping keep `stratus-runtime` and its users safe.
