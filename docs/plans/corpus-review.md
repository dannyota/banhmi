Status: Proposal. Not approved, not implemented.

# Corpus review delivery plan

## Goal and scope

Deliver the [corpus review workbench](../specs/corpus-review.md) as a bounded Vietnam-first review path. Build the private review API before the deferred human console. Preserve one fenced VNGCloud writer in Vietnam, immutable releases, the five public MCP tools, and the existing AWS services for the other five jurisdictions.

This plan owns implementation tasks, dependencies, evidence, and remaining gates. The specification owns review behavior, authority, records, security contracts, public evidence, and the worked acceptance case. Approval of either document does not authorize deployment, corpus mutation, source-gap acceptance, or release promotion.

Other delivery plans may reference these slices and gates. This plan owns corpus-review tasks, evidence, and gates.

## Dependencies and order

The work depends on the proposed contracts in the corpus write path, workflows, conversion, parts, database, embedding, and VNGCloud hosting specifications. Resolve conflicts in those contracts before dependent implementation.

1. Freeze packet, action, risk, and public evidence interfaces before storage or transport work.
2. Build storage, fencing, Vietnam-local authority, and recovery journal support before enabling decisions through the API.
3. Build the private review API with decision application disabled until transport, identity, and certificate controls pass their gates.
4. Enable owner decisions only after the approval service and writer application paths pass independent adversarial review.
5. Run the worked case through a candidate release before expanding review kinds.
6. Defer the human console until the API path is accepted. The console adds no decision authority.

Implementation preserves separate author and reviewer ownership under [repository roles](../../AGENTS.md#roles-and-dispatch). The integrating plan schedules independent milestone review. Mutation, credentials, authentication, certificate, journal, service authorization, and release paths require adversarial review. The primary inspects each handoff and its evidence before dependent work starts.

## Delivery slices

| Slice | Owner and scope | Dependency | Required hosted evidence before dependent work |
|---|---|---|---|
| 1. Example and contracts | One implementation owner defines typed packets, actions, risk rules, public summaries, the six-clause synthetic fixture, and a protected capture manifest for both originals. | Approved interface freeze. | Schema and citation snapshots; per-clause attribution negatives; date boundaries; identity ambiguity; missing-original failure. The real case needs hash-pinned public captures and human expectations. |
| 2. Storage and fencing | Separate owners implement review events, Vietnam audit and grants, the independent journal, local service attestations, and inherited fenced application. Database changes stay with the database slice owner. | Slice 1 interfaces. | Separate generation lanes; disposable PostgreSQL clean and upgrade checks; stale fence, epoch, and report rejection; duplicate receipts; conflicts; expiry; revocation races; no-build intake; missing-adjudication rejection. Restore an older backup after a revoked grant and an applied writer decision. Recover the revocation, reviewer mapping, and human audit from the journal. A missing tail keeps authority fenced. |
| 3. Review API | One API owner implements mutual TLS, device lifecycle, the private agent API, and the Vietnam approval API with decision application disabled. Hosting owns listener and network composition. | Slices 1 and 2. | A synthetic certificate authority proves that missing, expired, revoked, and wrong-issuer certificates fail handshakes. Stale certificate revocation lists, TLS resumption, and backend bypass fail closed. Prove renewal, lost-device revocation, human and service separation, multi-factor authentication and session binding, cross-token denial, scope, rate, and byte limits, prompt-injection defense, no arbitrary URL fetch, privacy sentinels, and stale-packet rejection. Console browser checks remain deferred. |
| 4. Owner decisions through the API | Separate authority and writer owners implement scoped grants, explicit owner authorization through operator transport, independent checks, one-use approval permits, the five quarantine actions, supersession, and reversion. | Slice 3 controls and owner decisions below. | Fake human identity and multi-factor authentication, API, and database cases; signed opaque reviewer keys with a separate issuer; no identity fields in writer or agent responses; journal failure before acknowledgement or permit; timeout races; commit before signal; response loss; live authority checks; in-flight revocation; late attempts; named-gap exclusion; non-binding OCR. |
| 5. Candidate and public evidence | Release and evidence owners carry the worked case through immutable assessments, candidate selection, and additive output from the five public tools. | Slice 4 and protected worked-case inputs. | Protected real-document checks; exact target, date, and citation goldens; unchanged retrieval floors; no whole-document repeal; scoped review markers; retained-release reads; rollback and restore fencing. Rejected language inputs reach neither embedding nor corpus queries. Review metadata cannot bypass the language gate. |
| 6. Bounded expansion | Owners for each review kind add encoding, parts, OCR, metadata, and subjects, then run the human precision sample and rule-improvement loop. | Accepted Slice 5 evidence. | Per-kind fixtures; different-model check provenance; zero autonomous agent acceptance; complete accounting; reviewed holdout results; unchanged contracts for the other five jurisdictions. |
| Later console | A separate interface owner builds the human console on the accepted review API. | Accepted API path and a separate approved console slice. | Browser login and multi-factor authentication, bounded continuation, keyboard and accessibility, stale-packet, and per-item confirmation checks. The console gains no new decision authority. |

## Evidence and activation gates

Use the [hosted evidence standards](../../instructions/verification.md#evidence-standards). The current default CI has no real corpus, workflow, certificate, or protected fixture inputs. Add isolated protected lanes before claiming those results. A missing original, skipped test, absent run, or unavailable reviewer blocks its gate.

For every slice, record the exact commit, workflow run, inputs, and scope. Pull request jobs receive no production credentials or real account data. Keep synthetic database credentials and certificate material confined to the job that creates them. Review generated database output before dependent work.

GitHub-hosted checks must cover repository hygiene, build, vet, short tests, applicable browser tests, disposable PostgreSQL integration, corpus schema generation and drift, isolated corpus fixtures, and retrieval evaluation for every affected jurisdiction. A short test cannot substitute for database, race, recovery, real-document, or retrieval evidence.

Live Google and multi-factor authentication compatibility, certificate and revocation enforcement, Vietnam residency controls, journal recovery, approval-service authorization, target-host capacity, protected real-document behavior, and release promotion need separate authorized proof after CI. Only a successful authorized deployment and post-deployment check can establish deployed behavior.

No task may apply a review decision to production or promote a release before all prior slice gates pass, the exact commit passes required hosted checks, and the owner authorizes the specific external action. Retain the prior release and its artifacts for rollback under the specification's retention contract.

## Remaining owner decisions

1. Approve scoped human delegation after an owner-only launch. The recommendation is separate technical, legal-assertion, and gap grants, with an independent human check for legal meaning. Keep critical reprocess adjudication and promotion owner-only under the write-path authority decision.
2. Allow any two-agent auto-application. The recommendation is none initially, including subjects and metadata. Measure proposal precision by kind first. A later non-legal exception needs a separate policy decision and cannot override gap authority.
3. Approve internal application authorization with one-use signed permits and a maximum 60-second in-flight window. The recommendation is approval only after adversarial review of current-authority checks, journal durability, revocation, and fenced application. The permit stays within the private Vietnam boundary.
4. Approve the priority and staffing targets. The recommendation is the P0, P1, and P2 targets, a 20% backlog and audit reserve, and the next-daily-candidate target. Workflow question 1 retains reminder and expiry approval. Measure backlog before expanding reviewers or formats.
5. Approve 30-day Vietnam-only aggregate exposure buckets and audit retention. The recommendation is aggregate counts with no user-level linkage, plus decision-audit retention while evidence references the decision and for at least one year after the last reference. Keep recovery records while a backup or decision needs them. Resolve account deletion and backup retention before activation. Public provenance contains no reviewer identity.

The owner must also resolve the source-gap policy through its existing authority before any accepted-gap action. Release promotion remains a separate owner action. VNGCloud writer placement and residency are settled. Reconcile the hosting recovery contracts before implementation.

## Missing inputs and completion status

The worked case still needs captured originals, hashes, rendered pages, and commencement proof. Delivery also lacks measured queue size and staffing, proposal precision by kind, verified live auth and corpus state, public-packet personal-data classification, target-host capacity, a selected certificate revocation verifier, device and multi-factor authentication compatibility, hosted protected fixtures, and confirmed deployed recipes.

No delivery slice has started or passed its evidence gate. Implementation remains proposed. Do not report corpus behavior as validated until real-document checks pass, or deployed behavior until the authorized service passes post-deployment checks.
