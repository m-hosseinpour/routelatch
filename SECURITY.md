# Security policy

## Reporting a vulnerability

Please report security issues **privately** through GitHub Security Advisories:

1. Open the repository on GitHub and go to the **Security** tab.
2. Choose **Report a vulnerability** (Private vulnerability reporting).
3. Describe the issue, affected version or commit, and the smallest
   reproduction you can provide.

Do not open a public issue, pull request or discussion for a suspected
vulnerability. Please do not include real share links, subscription URLs,
credentials or private destination lists in a report; replace them with
synthetic equivalents if possible. Reports are reviewed as soon as possible;
there is no fixed response-time commitment.

## Supported versions

There is no tagged release yet. Fixes are made on the `main` branch, and only
the current `main` is supported. Packaged or forked builds are supported by
whoever produced them.

## Security-relevant scope

Reports are especially useful for:

- approved traffic taking a direct path after a pool failure (a fail-open);
- a way to make the listeners bind to a non-loopback address or to reach the
  management socket without the same-uid check;
- disclosure of credentials, subscription URLs or carried destinations in the
  log, state, backups, proposals or CLI/JSON output;
- TLS verification being disabled, bypassed or downgraded by an imported
  node, or an HTTPS request being intercepted or replayed;
- mode changes that leave a connection alive under the old policy, or a
  reported successful change that had not finished;
- path traversal, symlink or permission problems in the state/config
  directories;
- terminal escape sequences from untrusted node names reaching the terminal or
  log unsanitized.

The known and deliberate limitations are listed in the README ("Security and
privacy" and "What it does not cover"). In particular, the loopback listeners
have no authentication, public exits are untrusted, and RouteLatch does not
protect traffic that never reaches its listeners. These are design choices, not
vulnerabilities, unless a report shows them behaving differently from the
documented contract.

## Disclosure

Please give the project time to investigate and prepare a fix before publishing
details. The maintainer will coordinate disclosure through the advisory.
