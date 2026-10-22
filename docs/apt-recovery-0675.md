# One audited APT recovery for 0.67.5

This procedure publishes the four original Debian packages from release run 39602441102, attempt 1, with the corrected V2 publisher. It never retries that run, changes its failed/skipped APT conclusions, creates a release or tag, updates Homebrew, rebuilds a package, or changes VERSION. The old Release40 gate remains failed. Root must separately audit the new signing and deployment job/check pairs before treating the new recovery as successful.

Git checkout rules preserve LF bytes for the exact publisher, builder, golden-facts and VERSION files whose bytes the recovery validates. This keeps their SHA checks identical on Windows checkouts with core.autocrlf=true; the integrity checks remain mandatory.

The checked-in scripts/apt_recovery_0675_golden.json is SHA-pinned by the validator. It contains the externally frozen original tag/commit/PR72 identity, all forty job/check pairs (38 successes, APT sign failure 119240033891 and APT deploy skip 119240838104), public release 419571329 and its eleven asset identities, the original artifact and four DEB hashes, the exact failed-log SHA, and all 168 retained APT paths. Those expected identities do not come from the API response being validated.

## Approve the new procedure after its own CI

The new workflow and validator must first pass their own complete CI30 on the recovery PR. The previously successful publisher-fix CI for PR73 is not accepted as the recovery procedure CI.

After that PR is merged, independently freeze a compact publisher approval with this exact schema:

    {
      "schemaVersion": 1,
      "purpose": "apt-recovery-0675-publisher-approval",
      "goldenSha256": "ac1e9818c77fac1d412e9d583d32e1f8642a832024ea69df57192d373e5b9a67",
      "publisher": {
        "dispatchSha": null,
        "headSha": null,
        "treeSha": null,
        "ciRun": null,
        "pairs": null,
        "pr": null,
        "merge": null
      }
    }

This is a non-executable illustration; every null must be replaced with independently approved real evidence. ciRun is the exact GitHub API run projection (IDs/nodes, repository, workflow, suite, attempt, event, created time, head/tree and canonical URLs). pairs contains all thirty externally pinned job/check projections, using the job and check API field names. It must use the exact thirty known CI names from the golden source, including the macOS headless timeout arguments. pr is the merged recovery PR's full identity and head/base projections. merge is the git/commits projection with exact SHA, node, URL, tree and ordered parents. Both the merge tree and local checkout tree must equal the tested CI tree. The PR base is the approved publisher-fix merge f0f606df2223db01b7bf16fbee989cd414e5e3d6.

Root supplies four workflow inputs: approved-publisher-sha, ci-run-id, approval-json and approval-sha256. Hash the exact UTF-8 bytes transmitted as approval-json; the workflow writes those bytes without adding a newline. Keep that compact JSON at most 64 KiB. Do not duplicate the golden facts in it, use placeholder identities, infer approval from elapsed time, or dispatch an unreviewed branch. The approved SHA must be the fresh current main commit, actual GITHUB_SHA and checked-out HEAD. Only a manual dispatch on main, attempt 1, in rosseca/kilo-proxy is accepted. A moved main causes rejection, including a final check immediately before Pages deployment.

## Validation and signing boundaries

The validation step has only read permissions for contents, actions and checks. It receives no APT signing secret. It verifies the pinned publisher/builder bytes before importing them, hashes the external approval before parsing it, rejects duplicate/nonfinite JSON, checks every original and procedure job/check identity, and revalidates the immutable tag, original source/PR72, stable/latest public metadata and original artifact. Public asset metadata is not public-byte verification.

Only original artifact 12348047748 is eligible: exactly 31,476,759 bytes with SHA256 4e514ae82c0e66ab088aa27ffb4a703fc2139ebac28d4b02b0e5de70a09f3555. Raw size and digest are checked before any ZIP parser or package tool. The ZIP must contain exactly the four expected regular DEBs and DEBIAN-SHA256SUMS.txt. Every DEB size/SHA, the original checksum manifest and dpkg package/version/architecture are verified. The full failed job log must retain its exact frozen SHA; an unavailable or changed log fails instead of using fuzzy matching. Source/main/public metadata is checked again after the download. The ZIP is downloaded once; new CI artifacts are never substituted.

Only the later signing step imports the existing KILO_APT_SIGNING_KEY into its fresh mode-0700 GNUPGHOME, unsets the secret variable and removes the isolated keyring on exit. No new secret, certificate, host keyring or bootstrap variable is required. The primary fingerprint remains 15AAF121DBFB06322EF1D7C8099722B1E76932AC. Bootstrap is always false.

The wrapper validates all 168 old path/size/SHA identities in the publisher's authenticated previous tree before building, then checks them again in the completed site before upload. All four original DEBs must also remain exact in that site. The pinned V2 publisher keeps its outer and inner signature checks, bounded metadata archive and exact-size/SHA payload materialization. The 400 MiB snapshot and 900 MiB complete-site limits remain unchanged. A failed validation prevents Pages upload/deployment; it never prunes, resets or replaces an old package's bytes.

The workflow has only sign and deploy jobs, uses the existing APT publication concurrency group, and gives Pages/id-token write permissions only to deployment. Runtime inputs are environment/file data, never interpolated into shell source. Root freezes the actual new run, workflow, suite and two job/check identities after dispatch, and audits an independent final snapshot. No automatic retry or promotion of the old failed Release40 is part of this procedure.
