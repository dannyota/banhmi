Proposal. Not approved, not implemented.

# Embedding models and runtime

Recommend **Granite-Embedding-311M-Multilingual-R2, 768 dimensions, ONNX Runtime INT8** as the replacement candidate. Keep Qwen3-Embedding-0.6B FP16 in service until the candidate passes the gates below. A newer release or a leaderboard lead does not prove better Vietnamese legal retrieval.

Granite's weights use Apache-2.0, but its Gemma-derived tokenizer is subject to Gemma terms. The complete runtime bundle is not Apache-only. Record the tokenizer terms and applicable notices in the pinned recipe and distributed artifacts. [IBM tokenizer attribution](https://huggingface.co/ibm-granite/granite-embedding-311m-multilingual-r2#tokenizer-attribution).

## Goal and limits

Select one commercially usable, open-weight model for document and query embeddings within each release. Prove Vietnamese evidence retrieval, CPU query latency, memory use, and batch/query parity before migration. Preserve native citations and every disclosed evidence gap.

The owner has settled these boundaries:

- Tra cứu quy định at `tracuuquydinh.vn` serves Vietnam from VNGCloud in Vietnam. The five other country services stay unchanged on AWS. Their later migration needs separate approval.
- Vietnam questions are Vietnamese with diacritics or a document number; the serving layer validates the question before any embedding or corpus query. English is interface text only. Do not translate questions or add query generation.
- Run only the query embedder on our serving host. No answer model, generative model, or reranker runs on our servers.
- Never send user questions, query vectors, or query logs to a third-party processor. Public source documents may use cloud batch embedding. Hosted evaluation uses public benchmark questions and authored test questions, never user traffic.
- Preserve `guide`, `corpus_status`, `quality_gaps`, `search`, and `document`. Future subjects reuse the recipe contract. Do not build additional subjects or country products here.

These product decisions follow [WEBSITE.md:10](../design/WEBSITE.md#L10). The hosting location and no-reranker rule are settled owner inputs. This proposal does not reinterpret the cited cross-border law.

The model must support Vietnamese first and leave a credible path for Thai, Indonesian, Malay, and English. Khmer is optional. Existing Malaysia and Cambodia evaluation uses English, so passing those suites does not establish Malay or Khmer quality. The current language map is recorded in [ARCHITECTURE.md:14](../ARCHITECTURE.md#L14).

Document extraction, chunk boundaries, provision validity, amendment targets, relation attribution, redlines, release storage, and subject filters belong to sibling designs. An embedding change cannot repair the known 09/2020 Điều 25 repeal or the misattributed 50/2024 amendment clauses. Those examples remain corpus defects, as recorded in [WEBSITE.md:206](../design/WEBSITE.md#L206).

## Current state and evidence

“Coded” means inspected source. “Recorded validation” means the repository reports a real-corpus measurement; this design did not reproduce it. No deployed state was independently checked.

| State | Finding | Repository evidence |
|---|---|---|
| Coded | Canonical model tag is `qwen3-embedding-0.6b`; dimensions are 1,024. | [config.go:17](../../pkg/base/config/config.go#L17) |
| Coded | `cmd/embedder` selects Qwen3 ONNX FP16 assets and passes the canonical model and dimensions. | [embedder_onnx.go:15](../../cmd/embedder/embedder_onnx.go#L15) |
| Coded | Query formatting uses a legal retrieval instruction. Documents do not receive that instruction. | [embed.go:44](../../pkg/rag/embed/embed.go#L44) |
| Coded | The ONNX adapter assumes Qwen decoder inputs, empty attention caches, and last-token pooling. Another model is not a filename-only substitution. | [onnxembed.go:8](../../pkg/rag/embed/onnxembed/onnxembed.go#L8), [onnxembed_onnx.go:283](../../pkg/rag/embed/onnxembed/onnxembed_onnx.go#L283) |
| Coded | Kaggle bulk uses Qwen3 ONNX FP16, last-token pooling and normalization. Its configured maximum is 8,192 tokens, below the model's advertised 32K context. | [kernel_embed.py:4](../../pkg/rag/embed/kagglebatch/kernel_embed.py#L4), [kernel_embed.py:46](../../pkg/rag/embed/kagglebatch/kernel_embed.py#L46) |
| Coded, incompatible | SageMaker batch still loads `BAAI/bge-m3`. The caller uses the canonical model tag. Do not use this path for Qwen vectors. | [embed_script.py:31](../../pkg/rag/embed/sagebatch/embed_script.py#L31), [embed_all.go:130](../../pkg/pipeline/embed_all.go#L130), [embed_all.go:208](../../pkg/pipeline/embed_all.go#L208) |
| Coded, incompatible default | `cmd/embed-backfill` defaults to BGE-M3 ONNX files while reporting the canonical tag. | [embedder_onnx.go:14](../../cmd/embed-backfill/embedder_onnx.go#L14) |
| Coded, legacy | An OpenVINO adapter exists and defaults its reported model to BGE-M3. Its existence does not prove Qwen or Granite parity. | [ovembed.go:15](../../pkg/rag/embed/ovembed/ovembed.go#L15), [ovembed_openvino.go:133](../../pkg/rag/embed/ovembed/ovembed_openvino.go#L133) |
| Coded | Dense and BM25 ranks use reciprocal rank fusion (RRF). Compiled candidate pools are 50 per arm; the retriever fallback is 100. | [rrf.go:29](../../pkg/rag/retrieve/rrf.go#L29), [RAG.md:62](../design/RAG.md#L62) |
| Coded | Both legacy and new source-schema vector columns remain `vector(1024)`. New release manifests also constrain dimensions to 1,024. | [gold/schema.sql:31](../../sql/gold/schema.sql#L31), [gold/schema.sql:119](../../sql/gold/schema.sql#L119), [ingest/schema.sql:494](../../sql/ingest/schema.sql#L494) |
| Coded, unexecuted source pass | Singleton query admission and release domain contracts exist. Hosted native checks and durable release adapters remain gates. | [main.go:88](../../cmd/embedder/main.go#L88), [corpus-redesign-source.md:15](../plans/corpus-redesign-source.md#L15) |
| Recorded validation | Six accepted retrieval baselines exist. A forced Vietnam rebuild changed chunks and reduced recall, even with the code change reverted. | [PLAN.md:89](../../PLAN.md#L89), [PLAN.md:659](../../PLAN.md#L659) |
| Recorded operation | Qwen FP16 uses about 2.3 GB per process; the plan records a roughly four-minute cold start. The AWS task reserves 2,400 MiB. These are not VNGCloud measurements. | [PLAN.md:797](../../PLAN.md#L797), [PLAN.md:513](../../PLAN.md#L513), [task definition:37](../../deploy/aws/ecs-task-definition-embedder.json#L37) |
| Coded CI limit | The default checks explicitly omit ONNX and corpus evaluation inputs. Passing default CI cannot select an embedding model. | [reusable-checks.yml:169](../../.github/workflows/reusable-checks.yml#L169) |

## Candidate survey

The following are publisher claims, not banhmi results. Parameter counts are approximate. Context limits count each model's own tokens, including prompts and special tokens. Matryoshka training permits a shorter vector prefix followed by normalization; arbitrary truncation does not.

Scores retain their published benchmark labels. MTEB means Massive Text Embedding Benchmark. MMTEB is its multilingual extension. MRR means mean reciprocal rank; nDCG means normalized discounted cumulative gain. A retrieval score is not an all-task mean, and different task subsets are not directly comparable. CPU feasibility below is an engineering estimate for short queries, not measured latency.

| Model and first-party source | Parameters | Dimensions; Matryoshka | Context | Weight license | Language evidence | Published retrieval score or stated gap | CPU assessment |
|---|---:|---|---:|---|---|---|---|
| [Qwen3-Embedding-0.6B](https://huggingface.co/Qwen/Qwen3-Embedding-0.6B) | 0.6B | 1,024; 32–1,024 | 32K | Apache-2.0 | 100+ languages | MTEB multilingual retrieval 64.64 | Existing control; test INT8 |
| [Qwen3-Embedding-4B](https://huggingface.co/Qwen/Qwen3-Embedding-4B) | 4B | 2,560; yes | 32K | Apache-2.0 | 100+ | MTEB multilingual retrieval 69.60 | About 4 GB of INT8 weights alone; reject under query budget |
| [Qwen3-Embedding-8B](https://huggingface.co/Qwen/Qwen3-Embedding-8B) | 8B | 4,096; yes | 32K | Apache-2.0 | 100+ | MTEB multilingual retrieval 70.88 | About 8 GB of INT8 weights alone; reject |
| [BGE-M3](https://huggingface.co/BAAI/bge-m3) | 568M | 1,024; no advertised Matryoshka | 8,192 | MIT | 100+ | Dense Legal Zalo Accuracy@10 89.21%, MRR@10 68.22% in the fine-tune author's comparison below | Plausible INT8 encoder; no published target-host result |
| [Vietnamese_Embedding](https://huggingface.co/AITeamVN/Vietnamese_Embedding) | About 568M, BGE-M3 base | 1,024; no advertised Matryoshka | 2,048 | Apache-2.0 | Vietnamese fine-tune; other languages unverified | Legal Zalo Accuracy@10 95.68%, MRR@10 81.81% | Plausible; long-input limit |
| [Vietnamese_Embedding_v2](https://huggingface.co/AITeamVN/Vietnamese_Embedding_v2) | About 568M, BGE-M3 base | 1,024; no advertised Matryoshka | 2,048 | Apache-2.0 | Vietnamese; retained multilingual quality unverified | Legal Zalo Accuracy@10 95.78%, MRR@10 81.49% | Plausible INT8 challenger; length and parity gates |
| [multilingual-e5-large-instruct](https://huggingface.co/intfloat/multilingual-e5-large-instruct) | About 560M | 1,024; no advertised Matryoshka | 512 | MIT | XLM-R multilingual family, roughly 100 languages | MTEB multilingual retrieval 57.12 in [Qwen's published comparison](https://huggingface.co/Qwen/Qwen3-Embedding-0.6B) | Feasible class, but context too short |
| [EmbeddingGemma](https://ai.google.dev/gemma/docs/embeddinggemma/model_card) | 300M class | 768; 512/256/128 | 2,048 | Gemma terms | 100+ spoken languages | Card reports multilingual MTEB all-task mean 61.15; retrieval-only value unverified here | On-device candidate; no FP16 activations |
| [Arctic Embed L v2](https://huggingface.co/Snowflake/snowflake-arctic-embed-l-v2.0) | 568M, not 303M total | 1,024; 256 | 8,192 | Apache-2.0 | 74 card language tags; per-language legal quality unverified | nDCG@10: BEIR(15) 55.6, MIRACL(4) 55.8 | Plausible INT8; larger than Granite |
| [Granite-Embedding-278M-Multilingual](https://huggingface.co/ibm-granite/granite-embedding-278m-multilingual) | 278M | 768; no advertised Matryoshka | 512 | Apache-2.0 | 12 languages; no advertised Vietnamese coverage | MTEB multilingual retrieval(18) 52.2 in [IBM's R2 report](https://arxiv.org/html/2605.13521v1) | Small, but language and context reject it |
| [Granite-Embedding-311M-Multilingual-R2](https://huggingface.co/ibm-granite/granite-embedding-311m-multilingual-r2) | 311M | 768; 512/384/256/128 | 32,768 | Apache-2.0 | 200+ pretraining; 52 enhanced languages include all six requested languages | MTEB multilingual retrieval(18) 65.2 | Best replacement hypothesis; official ONNX/OpenVINO exports |
| [Granite-Embedding-97M-Multilingual-R2](https://huggingface.co/ibm-granite/granite-embedding-97m-multilingual-r2) | 97M | 384; unverified Matryoshka support | 32,768 | Apache-2.0 | Same broad language claims | MTEB multilingual retrieval(18) 60.3 in [IBM's report](https://arxiv.org/html/2605.13521v1) | Lowest memory hypothesis; defer for quality |
| [Jina embeddings v5 text small](https://huggingface.co/jinaai/jina-embeddings-v5-text-small) | 677M | 1,024; 32–1,024 | 32,768 | CC-BY-NC-4.0 | 119+ | MMTEB all-task mean 67.7; retrieval-only value unverified here | Plausible size; commercial license disqualifies open weights |

Granite multilingual R2 and Jina v5 are confirmed newer releases than Qwen3's embedding family. This survey does not claim to enumerate every new model. IBM's [technical report](https://arxiv.org/html/2605.13521v1) distinguishes the 311M model's Matryoshka support from the compact model. Do not infer identical compression behavior from a family name.

The Vietnamese fine-tune scores are author-reported on the Legal Zalo 2021 training split used as evaluation. The author says the model did not train on that dataset. That is useful selection evidence, not an independent contamination audit. The v2 card also links a reranker in its opening base-model sentence while naming BGE-M3 elsewhere. Verify the pinned weights, configuration, and tokenizer before export. [Fine-tune card](https://huggingface.co/AITeamVN/Vietnamese_Embedding_v2).

Use [VN-MTEB](https://aclanthology.org/2026.findings-eacl.86/) for broader Vietnamese coverage and the [cross-domain Vietnamese retrieval study](https://aclanthology.org/2026.findings-eacl.110/) for domain-sensitivity evidence. Neither replaces banhmi's provision, validity, and citation evaluation. Any score reproduced later must name dataset revision, split, metric, and dense-only versus hybrid mode.

EmbeddingGemma permits commercial use subject to [Gemma terms](https://ai.google.dev/gemma/terms); those terms are not Apache-2.0. Its [weight card](https://huggingface.co/google/embeddinggemma-300m) excludes FP16 activations. Jina v3 and v5 open weights are non-commercial; v4 carries a research license. Separate commercial distribution does not change the open-weight license. Reject that dependency under the stated requirement. [Jina licensing guidance](https://jina.ai/embeddings/).

## Selection and rejected options

Run exactly three models in the first bake-off. Precision and dimension variants do not add models.

| Candidate | Purpose | Initial recipe |
|---|---|---|
| Granite-Embedding-311M-Multilingual-R2 | Recommended replacement hypothesis: smaller encoder, long context, target-language support, Apache-2.0 weights with Gemma tokenizer terms, published runtime exports | 768 dimensions; CLS pooling; normalized output; publisher input convention; CPU INT8 versus FP32 reference |
| Qwen3-Embedding-0.6B | Control and least disruptive alternative if quantization alone solves latency | Existing 1,024 dimensions, legal query prompt, last-token pooling; existing FP16 control plus CPU INT8 |
| Vietnamese_Embedding_v2 | Tests whether Vietnamese specialization beats a general multilingual model | 1,024 dimensions; pinned Sentence Transformers pooling and preprocessing; INT8 versus FP32 reference |

Prefer Granite only after measured acceptance. If it fails, keep Qwen; do not choose the next leaderboard entry automatically. The Vietnamese fine-tune may win Vietnam without becoming a common ASEAN default. Future countries require their own gates.

Reject larger Qwen sizes for the initial CPU budget. Reject E5 and Granite R1 for short context; Granite R1 also lacks the required advertised language coverage. Defer base BGE-M3 and Arctic because the three slots test the current model, a newer efficient encoder, and Vietnamese specialization. Defer EmbeddingGemma because its shorter context and activation constraints add risk without a confirmed Vietnamese legal advantage. Defer Granite 97M until 311M misses the resource gate. Prefer v2 over v1 as the specialization challenger because of its broader fine-tuning corpus, despite v1's slightly higher reported legal MRR.

Reject fine-tuning our own model in this pass. It would entangle model selection, training-data rights, and evaluation leakage. Reject hosted embedding APIs because queries must remain on our host. Reject a larger bulk-only model because its vectors would not share the query model's space.

## Embedding recipe and runtime parity

A recipe identifies the vector space and its production method. It includes immutable weight revision and hashes, tokenizer files and hashes, weight and tokenizer terms and notices, pooling, query/document templates, Unicode normalization, maximum input lengths, overflow behavior, dimensions, normalization order, export graph hash, quantization method and calibration digest. Runtime image, library version, execution provider, CPU instruction set, and thread settings belong in the evaluation manifest.

Use one checkpoint and tokenizer for queries and documents. Different CPU and GPU numeric formats are allowed only as an explicitly certified pair. Do not call identical dimensions proof of compatibility. Bind a release to the document recipe, query recipe, and their parity report. A changed prompt or pooling rule requires a new recipe even when weights stay fixed.

For Qwen, preserve the current query prompt and unprefixed document convention during the control run. For Granite, follow its CLS pooling and publisher input convention. Do not pass Granite through the Qwen cache-input graph or `FormatQuery`. The same applies to the Vietnamese encoder. Capture authoritative Sentence Transformers modules rather than guessing pooling from the base model. The current hardcoded interfaces are evidenced above.

Run parity checks before full embedding:

1. Pin 1,000 public text inputs from all six country corpora across Vietnamese, English, Indonesian, and Thai, covering all input-length buckets. Include Vietnamese decomposed accents, document numbers, repeated headings, tables, empty inputs, and limit boundaries. Add authored Malay and optional Khmer probes without claiming corpus readiness.
2. Compare token IDs, attention masks, special tokens, input hashes, pooling position, output order, and dimensions across Go query serving and batch exporters. Compare each role against its own reference; query and document prompts need not match.
3. Require finite, nonzero vectors and normalized length within 0.0001 of one before storage. Require FP32/FP16 export cosine agreement at least 0.999 per valid probe. For INT8, require median at least 0.995 and minimum at least 0.98 against the full-precision reference. These are proposed gates, not observed accuracy.
4. Require no accepted golden hit to disappear solely through quantization or runtime choice. Report exact-search top-20 neighbor overlap; target at least 98% mean overlap. The retrieval gate remains decisive even if vector similarity passes.
5. Repeat with batch sizes 1, 8, and 32, mixed lengths, both padding shapes allowed by the recipe, and concurrent queries. Fail rather than silently switch providers or precision. Record the actual executed graph types.

## CPU query service

Recommend ONNX Runtime with 8-bit integer (INT8) weights and supported quantized operators, with 32-bit floating-point (FP32) accumulation where required. Keep the existing 16-bit floating-point (FP16) control for comparison and rollback. “FP16 file” does not imply FP16 CPU computation: the [ONNX Runtime guidance](https://onnxruntime.ai/docs/performance/model-optimizations/float16.html) documents CPU operator limitations and mixed-precision casts. Measure the pinned graph rather than assuming a twofold memory or speed gain.

Use dynamic INT8 as the first encoder export experiment. Compare a calibrated static INT8 export only if dynamic INT8 fails the latency gate. Calibrate solely on a disjoint public-document sample. [ONNX Runtime quantization guidance](https://onnxruntime.ai/docs/performance/model-optimizations/quantization.html) describes both approaches and hardware-dependent accuracy and speed effects. Neither is accepted before parity and retrieval checks.

| Runtime | Decision | Reason and rejection condition |
|---|---|---|
| ONNX Runtime CPU | Default | Preserves the existing service boundary and offers a portable CPU path. A new encoder adapter and tagged hosted build are still required. Reject an export with unsupported operators or hidden truncation. |
| OpenVINO CPU | Controlled alternative on confirmed compatible host CPU | Benchmark the same model and recipe. Adopt only if every quality gate passes and p95 improves by at least 20% within memory limits. Do not reuse the legacy BGE-M3 identity. |
| FP16 on CPU | Control or measured fallback | Smaller stored weights alone do not establish faster CPU inference. Keep only if it passes the same resource gate. |
| GPU query serving | Rejected for this design | The requirement is own-host CPU queries. Batch GPU use remains separate. |

OpenVINO supports reduced-precision execution, but storage precision and compute precision are distinct. Its [precision-control documentation](https://docs.openvino.ai/2025/openvino-workflow/running-inference/optimize-inference/precision-control.html) is the runtime reference. Host CPU support and an actual benchmark decide whether it improves this workload.

The following are proposed demand limits for hosting, not VNGCloud capacity claims. Resident set size (RSS) includes native inference memory, not only the Go heap. The p95 latency is the 95th percentile; report p99 separately to expose slower requests.

| Item | Initial target and acceptance workload |
|---|---|
| CPU allocation | Measure a 2 vCPU inference ceiling on `s2-general-4x8` (4 vCPU, 8 GiB); admit `s2-standard-2x8` only if concurrent load fits. Record CPU model, instruction flags, and throttling; set the reservation from measurement. |
| Resident memory | Granite INT8 planning range 1–2 GiB; maximum process RSS 2.5 GiB after a 30-minute soak; measure the final reservation for the single process |
| Short-query latency | p95 at most 500 ms and p99 at most 1 s, including local HTTP, tokenization, queueing, inference, and normalization, for inputs up to 256 tokens |
| Maximum-query latency | p95 at most 1 s for 257–512 tokens under the same steady load |
| Load | 2 requests per second for 30 minutes, bursts of 8, with concurrent PostgreSQL and application load; report rejection rate and successful latency together |
| Admission | Start with two active requests and four queued requests for the single runtime, 250 ms maximum queue wait, and a 2 s request deadline; measure thread settings without oversubscribing 2 vCPUs |
| Input | One text per query call; at most 512 tokens including prompt and special tokens; byte limit before tokenization; reject excess with an explicit error |
| Cold start | Readiness within 60 s from process start with artifacts already present; report artifact-fetch time separately; warm representative 32/128/512-token probes before readiness |
| Availability | No steady-load overloads or inference errors; burst excess returns a retryable overload response; no silent lexical-only fallback |

The weight-only INT8 lower bound for 311M parameters is about 0.31 GB. Tokenizer, unquantized layers, activations, native arenas, and concurrent requests explain why the RSS budget is larger. The estimate must be replaced by observed peak RSS, including startup, cancellation, and repeated largest accepted inputs.

Use one process and one loaded embedding model on the serving host, shared by website and MCP. Keep query batching at one initially. Do not wait for a batch to fill. Batch workers use separate processes, length buckets, and explicit token and memory ceilings.
Bulk document embedding runs on Hugging Face Jobs, pay as you go, with Runpod Flex as fallback under the [write-path decision](corpus-write-path.md#decisions). Kaggle and GitHub Actions free tiers serve evaluation and bake-offs only. Never submit user query traffic to any batch service.
Public document GPU runs may use FP16 or FP32 exports from the same weights if the exact batch/query pair passes parity.

Do not size production from a shared GitHub runner's latency. GitHub verifies reproducibility and resource regressions. A separate owner-authorized benchmark on the selected VNGCloud CPU must pass the demand limits before serving.
Size from measurement for PostgreSQL, one embedder, and the Go server on a dedicated small vServer. Start from `s2-general-4x8` (4 vCPU, 8 GiB), with `s2-standard-2x8` as the cheaper fallback if measurement allows, under the [hosting decision](vngcloud-hosting.md#workload-and-sizing).
Keep the 2 vCPU constrained bake-off as a comparison workload, not a fixed production reservation. Measure at 2 requests per second, bursts of 8, with concurrent PostgreSQL and application load. Grow only from measurement; no automatic 8 vCPU tier is required.
Before promotion, measure the single runtime's startup and steady-load peaks, database working set, application load, and maintenance allowance against hosting's requirement for at least 25% free headroom. No dual-runtime capacity is reserved.

## Long provisions and storage

Hold the existing chunk text, context prefixes, and citations constant during the primary comparison. Measure token-length distributions separately for every tokenizer. Report p50/p95/p99/max, overflow count, and which expected provisions overflow. Do not silently truncate any indexed text or query.

The first production recipe should admit document inputs up to 8,192 tokens including context, with every larger input explicitly rejected and reported to the chunk owner. A 32K model window permits later experiments, not permission to enlarge every chunk. Test supported 2K/8K/32K public inputs separately for retrieval of text near the end and for peak memory. The fine-tune's 2,048-token contract can disqualify it; do not shorten every other candidate's inputs to conceal that limit.

If a frozen snapshot contains over-limit chunks, publish the unchanged whole-snapshot result with those failures. An in-range subset is diagnostic only. A candidate needs full required chunk coverage to win. Any later chunking repair requires a new snapshot and a fresh Qwen control; never attribute gains from new chunk boundaries to the model.

Recommend full 768-dimensional normalized Granite vectors, with FP32 reference artifacts and `halfvec` serving storage only after the storage precision gate. If Qwen remains, keep 1,024 dimensions. Weight quantization, vector storage precision, and Matryoshka truncation are three different decisions.

| Representation | Raw coordinate bytes per vector | Per million vectors | Decision |
|---|---:|---:|---|
| Qwen 1,024 FP32 | 4,096 | 4.096 GB | Baseline |
| Qwen 1,024 half precision | 2,048 | 2.048 GB | Separate storage experiment |
| Granite 768 FP32 | 3,072 | 3.072 GB | Reference vectors |
| Granite 768 half precision | 1,536 | 1.536 GB | Recommended serving candidate |
| Granite 384 half precision | 768 | 0.768 GB | Deferred Matryoshka experiment |
| Granite 768 sign bits | 96 | 0.096 GB | Reject for initial serving |

These are decimal payload estimates, excluding row metadata, indexes, graph edges, replicas, and overlapping releases. The database design must measure those costs. The [pgvector reference](https://github.com/pgvector/pgvector#vector-type) documents storage and index limits; its HNSW limits are 2,000 dimensions for `vector` and 4,000 for `halfvec`. Thus full 4,096-dimensional Qwen8B also exceeds that half-vector index limit.

Compare half precision with FP32 at unchanged candidate depth and exact-search settings. Require every quality gate and no lost accepted golden hit. Do not enable approximate indexing or dimension reduction in the same experiment. Vietnam currently selects exact dense scanning via its jurisdiction descriptor, as coded in [jurisdiction.go:178](../../pkg/base/jurisdiction/jurisdiction.go#L178).

Matryoshka truncation needs prefix selection followed by normalization for both roles and a new recipe. Reject binary-only ranking initially: sign distance loses magnitude information, and there is no neural reranker to recover a candidate lost before fusion. Exact cosine rescoring of stored vectors is a database operation, not a served model, but it cannot recover unseen candidates. Any future binary shortlist needs its own recall gate and retained full vectors.

## Hosted bake-off

Run no local builds, tests, exports, databases, or evaluations. Add the hosted lanes below after design approval. Do not mistake the current source-only permission for approval to dispatch a batch job.

Freeze one immutable evaluation snapshot per country with source manifest, exact chunk bytes and IDs, context prefixes, citations, validity, relations, lexical vectors, retrieval settings, query preprocessing, goldens, and code commit. Capture current index counts and recipe hashes. Restore only corpus data to disposable databases; exclude auth data and user questions.

Use the existing `cmd/eval` matcher and accepted country goldens. Keep `expect_fail` cases excluded from aggregate scoring and list them separately. Keep relation-based credit visible; also report direct document and provision hits so graph credit cannot hide embedding loss. Existing metrics and exclusions are specified in [RAG.md:202](../design/RAG.md#L202). Add diagnostics alongside that contract, not a replacement scoring definition.

| Country | Golden file under `deploy/eval/` | Recall floor | MRR floor | Current-law precision floor | Abstention floor |
|---|---|---:|---:|---:|---:|
| Vietnam | `golden.json` | 0.90 | 0.66 | 0.99 | 0.95 |
| Malaysia | `golden_my.json` | 0.92 | 0.77 | 0.99 | 0.95 |
| Indonesia | `golden_id.json` | 0.78 | 0.60 | 0.99 | 0.98 |
| Singapore | `golden_sg.json` | 0.90 | 0.75 | 0.99 | 0.90 |
| Thailand | `golden_th.json` | 0.86 | 0.68 | 0.99 | 0.90 |
| Cambodia | `golden_kh.json` | 0.90 | 0.70 | 0.99 | 0.95 |

The floor authority is [Makefile:68](../../Makefile#L68); accepted results are in [PLAN.md:89](../../PLAN.md#L89), and golden paths come from [jurisdiction.go:191](../../pkg/base/jurisdiction/jurisdiction.go#L191). Freeze all three at the evaluation commit. Do not lower floors or rewrite goldens to admit a candidate.

1. Reproduce Qwen on all six frozen snapshots before comparing models. Report differences from the recorded baseline and resolve snapshot/configuration drift first. Pin top-k, pools, lexical weight, document cap, filters, as-of date, and exact/approximate search settings explicitly.
2. Run candidate preflight and parity before full public-document embedding. Export new vectors beside the control. Reuse unchanged Qwen vectors only when input and full recipe identity are proven. Compute all challenger vectors from scratch for the first run.
3. Run dense-only and hybrid modes for each model and country. Run BM25-only once per frozen snapshot as the invariant lexical control. The existing flags and pool probe are coded in [cmd/eval/main.go:75](../../cmd/eval/main.go#L75). Extend composition to select the candidate recipe; do not relabel it Qwen to fit the harness.
4. Report existing recall@8, MRR@8, current-law precision, abstention, per-case ranks, and pool recall@200. Add direct provision recall, known-gap counts, long-input coverage, citation completeness, embedding failures, and nDCG@10 on a separately judged set. Graded nDCG is new work, not a current harness capability.
5. Create 120 disjoint authored Vietnamese scenarios from public sources: 20 each for banking duties, technology controls, amendments and dates, long provisions, appendices/forms, and negative or ambiguous requests. Use 60 for tuning and seal 60 before model selection. Split by document family to reduce leakage. Human reviewers assign relevance and required citations; no answer model generates judgments.
6. Calibrate model-specific cosine abstention thresholds only on the development set, then freeze them. RRF scores are not absolute relevance scores. Keep existing lexical/query routing fixed. Diagnose BM25-only hits separately because they lack cosine evidence. The current gate distinction is documented in [RAG.md:170](../design/RAG.md#L170).
7. Require every existing floor on all six candidate snapshots for a common-model claim. Require no decline in Vietnam recall, MRR, current-law precision, or abstention versus the reproduced control, on both the accepted suite and the sealed set. Require the runtime, coverage, and parity gates too. Require no newly wrong current-law result in the added validity cases. Known corpus defects remain explicit and cannot be credited as fixed by ranking.
8. Require either at least two percentage points higher Vietnam MRR or at least 30% lower query p95 with unchanged Vietnam quality. Report paired bootstrap 95% intervals over cases with 10,000 resamples on the sealed set; do not call an uncertain quality gain proven. If the latency route wins, report it as a cost/latency improvement. If neither route passes, keep Qwen.
9. Repeat the winning recipe on three hosted CPU runs, then certify its exact CPU-query/GPU-document pair and half-vector storage separately. Archive per-case deltas, counts, hashes, vector probes, latency distribution, peak RSS, startup time, and spend under one evaluation manifest.

The same evaluation code must pass the five unchanged Qwen country services in disposable snapshots before a shared-code rollout. Challenger scores for those countries are evidence for later adoption, not authority to migrate them. A Vietnam-only fine-tune may pass Vietnam but must not be called ASEAN-ready. Malay needs a new reviewed retrieval set before any future Malay service; English Malaysia goldens are insufficient.

### Cost and execution assumptions

Prefer GitHub-hosted CPU jobs and GitHub-hosted GPU jobs for this bounded comparison. This keeps checks and artifacts together without depending on free GPU quota. Kaggle and GitHub Actions free tiers serve evaluation and bake-offs only. Production bulk document embedding uses Hugging Face Jobs, pay as you go, with Runpod Flex as fallback; legacy SageMaker and Kaggle adapters are not the selected production services. Evaluation jobs write only disposable candidate data. Corpus generation runs on AWS as the single writer per country dataset, orchestrated by Temporal Cloud; serving databases on VNGCloud and on AWS receive verified releases. The [write-path decision](corpus-write-path.md#decisions) approves Temporal Cloud Developer, pay as you go with no minimum, and the isolated AWS writer.

The recorded six-country counts total 381,955 chunks, with 142,086 in Vietnam ([PLAN.md:94](../../PLAN.md#L94)). Assume two new full-corpus embeddings, plus a bounded Qwen replay and parity work. At an unverified 10–30 chunks/second, the two challengers need about 7.1–21.2 GPU hours before startup and long-text overhead. Measure a 10,000-chunk stratified pilot before committing the full run.

Budget 30 GPU hours and 40 four-core CPU hours; cap inference at 2 vCPUs within the CPU runner and use the remainder for concurrent database and application load. GitHub lists Linux GPU at $0.052/minute and four-core CPU at $0.012/minute: $93.60 + $28.80 = $122.40. Reserve $25 for artifacts and transfers, giving a planning estimate of about $150 and a proposed hard ceiling of $200. These storage and throughput allowances are unverified estimates, not vendor quotes. [GitHub runner prices](https://docs.github.com/en/billing/reference/actions-runner-pricing).

Runner eligibility, GPU availability, quota, and included minutes are unverified. Split jobs below runner time limits and stop after the pilot if projected cost exceeds the ceiling. A whole-corpus Qwen re-embedding, poor long-text throughput, or retries can exceed this estimate. No model-training cost or production hosting cost is included.

## Retrieval without a reranker

No reranker anywhere; retrieval uses dense plus lexical fusion. The recorded experiment reduced recall from 92.7% to 82.9% in Vietnam, 94.3% to 92.9% in Malaysia, and 79.8% to 76.3% in Indonesia. Vietnam current-law precision fell to 86.7%. CPU INT8 cost was 0.76 seconds per pair, approximately 12 seconds for 16 candidates or 38 seconds for 50. The strict-current-law second round was cancelled, not measured. [PLAN.md:265](../../PLAN.md#L265).

The owner rejects the cost and latency and assigns evidence judgment to the user's own model. This is a product boundary, not proof that every user model beats every reranker. Do not reopen the old “larger model or GPU” triggers in this design.

Use dense plus BM25 fusion instead. Preserve `1/(60 + dense_rank) + lexical_weight/(60 + lexical_rank)` and existing per-country routing for the model comparison. After selecting the model, test deeper pools of 100 and 200 per arm against the pinned baseline. Recommend 100 only if quality does not regress and total search latency passes its separate budget; otherwise retain the measured setting. Keep top-k and document cap fixed in that experiment.

Apply trusted validity metadata before relevance ordering for current-law requests. Preserve the explicitly badged historical tail and as-of behavior. Unknown provision or part validity stays unknown; do not turn embedding similarity into a legal effect. Provisions and first-class revisioned parts share one validity target type supplied by the database contract. The corpus owner supplies validity and relation confidence. Return exact citations, source text, enclosing-provision or part links, validity dates and gaps, relation evidence, provenance, and the pinned release identity for the user's model to inspect.

Mark later removal candidates: the `-rerank-*` flags in [cmd/eval/main.go:82](../../cmd/eval/main.go#L82), its reranker client and replay paths in `cmd/eval/rerank.go` and `cmd/eval/rerank_experiment.go`, and `tools/rerankkernel/`. Preserve the recorded experiment and retain a neutral pool-diagnostics mode before removing any useful evaluation capability. No removal occurs in this proposal.

## Interfaces

| Sibling | Required contract |
|---|---|
| [Corpus write path](corpus-write-path.md) | Supply immutable chunk inputs and input hashes from the isolated AWS writer under Temporal Cloud. Accept a pinned recipe and resource bounds. Return exactly one accounted outcome per input, vector checksums, counts, parity report, and explicit overflow failures. Never publish partial vector coverage or use user questions as batch inputs. Own retries, fencing, publication, promotion, retention execution, and rollback. Orchestration and job services follow its approved decision. |
| [Corpus database](corpus-database.md) | Isolate vectors by country, chunk set, release, and complete recipe. Support at least 768 and 1,024 dimensions, FP32 reference and half-precision serving experiments, cosine search, exact-search evaluation, frozen lexical revision, and rejection of wrong recipes before search. Bind pins to evidence and recipe artifacts; track dense-search availability separately from `document` and lexical access. Return release and recipe identity on every read contract. Supply revisioned parts, the shared provision/part validity target, ordered blocks, table rows/cells/spans, and part-to-artifact byte spans. Own physical storage and replication. |
| [Document parts](document-parts.md) | Supply exact part text, context, citation, stable part/revision identity, validity and source authority for Phụ lục, Biểu mẫu, Mẫu số, tables, and attached files. Preserve ordered block, table-cell, and artifact byte-span mappings through chunk inputs. Identify attachment-only gaps. Receive tokenizer length and overflow reports. Parts owns identification, part-boundary parsing, citations, tracking, display, and structural splitting from conversion's blocks. Conversion owns extraction from bytes and normalization for every format. This design owns token budgets and does not invent text for image-only forms. |
| [VNGCloud hosting](vngcloud-hosting.md) | Supply Vietnam-only query processing and observability, a confirmed CPU instruction set, one private query runtime, immutable artifacts, and readiness. Start from `s2-general-4x8` (4 vCPU, 8 GiB), with `s2-standard-2x8` as the cheaper fallback if measurement allows. Prove at least 25% free headroom at 2 requests per second, bursts of 8, with concurrent PostgreSQL and application load. Set reservations and grow only from measurement; reserve no dual-runtime capacity. |

The recipe contract must replace the fixed-dimension constraints before Granite can publish. Do not hand-edit generated stores; [sqlc.yaml:1](../../sqlc.yaml#L1) identifies source SQL ownership. Existing release records are source-only contracts until the hosted and persistent-adapter gates in [corpus-redesign-source.md:19](../plans/corpus-redesign-source.md#L19) pass.

## Migration and rollout slices

Each slice requires independent review and GitHub-hosted evidence. A passing source test does not prove real-corpus quality or deployment. This file author does not implement these slices.

| Slice | Deliverable | GitHub-hosted verification before the next slice |
|---|---|---|
| 1. Recipe identity | Model metadata and hashes, tokenizer terms/notices in recipe and artifacts, role-specific templates, strict model selection; reject legacy BGE/Qwen mismatch | Synthetic tests for wrong tokenizer, pooling, dimensions and model; tagged builds; no network fallback; missing artifacts fail readiness; rejected Vietnam language inputs reach neither embedding nor corpus queries |
| 2. Runtime adapters | Encoder graph support, Qwen control, ONNX INT8 export, optional OpenVINO comparison, admission and overflow behavior | Public probe parity, batch ordering, cancellation, limits, peak RSS, cold/warm benchmarks; prove no outbound request during query serving |
| 3. Frozen evaluation | Six isolated corpus snapshots, unchanged goldens/floors, pilot cost report, three-model comparison | Full country reports and sealed Vietnamese evaluation; reject skips, empty snapshots, incomplete vectors, and exceeded cost projection |
| 4. Candidate release | New vectors beside old vectors; database and write-path siblings supply durable release support | Migration checks in disposable databases; zero cross-recipe reads; complete input/output hashes; storage precision gate; pinned requests survive promotion |
| 5. Promotion contract | Country-specific atomic selection of corpus, lexical revision, query recipe, runtime artifact, and serving configuration | Prove re-embedded dense search and old named-release `document`/lexical access with a visible dense-search gap; exercise a hold beyond 30 days with a simulated clock; protect held evidence and recipe artifacts; reject missing/mismatched runtimes, failed readiness, concurrent activation and interrupted rollback; unchanged five-country Qwen checks |
| 6. Vietnam rollout | Authorized VNGCloud deployment after concurrent-load and single-runtime capacity proof | GitHub records public-probe smoke results for website/MCP parity, old named-release search after promotion, readiness and rollback; live host capacity proof remains separate from CI simulation |

Re-baseline each country on its own frozen snapshot. Build the selected Vietnam release beside the old one. Keep the old release, vector index, query artifact, and configuration intact. Never write a new model into an old model's index, even when both have 1,024 dimensions.

Owner decision 18 requires only one embedding model on the serving host at a time. Before replacing the runtime, re-embed every release that stays in dense search, or retire older releases from dense search.
Retired releases remain available through `document` and lexical search, with a visible gap. Dense search requires a matching query runtime (same model, tokenizer, recipe and dimensions). No dual-runtime capacity is reserved.
Re-embedding creates new immutable releases with explicit predecessor mappings. Never overwrite old indexes or silently substitute a replacement for a named release. Old release IDs retain their original evidence with an explicit dense-search gap when their runtime retires.
Prepare and evaluate new bundles on batch infrastructure. Fence dense admission, drain in-flight dense requests, stop the old runtime, then start and verify the new runtime before reopening dense search. Persist each release's dense-search availability with the ordered activation record.
If the selected runtime fails, return explicit dense-search unavailability; never silently substitute another recipe or lexical-only results. Planned retirement is a visible mode, not an inference-error fallback.

Online retention is the active release plus at least two prior releases, kept for at least 30 days, plus any release under an explicit pin or hold.
Keep databases, evidence, and query-recipe artifacts for the entire window, including longer holds. A pin or hold preserves that closure, not a loaded old runtime. Preserve old named-release `document` and lexical access after dense-search retirement.
Block slices 5–6 until hosting and database contracts agree on the single-runtime transition and measured capacity. A failed re-embedding or unrecorded dense-search retirement blocks runtime replacement.
Rollback selects the complete retained tuple through a new ordered activation, applies the same dense-search transition to affected releases, and drains requests before replacing the single runtime. It does not restore auth state or rewrite evidence. Keep all retained evidence and recipe artifacts; repeat these gates separately for another country's later migration.

## Risks and unresolved evidence

| Risk | Required response |
|---|---|
| Published scores use different tasks or contaminated data | Freeze the benchmark revision and sealed legal scenarios; select from paired banhmi results |
| Fine-tune forgets non-Vietnamese languages or truncates provisions | Run all country reports and tokenizer overflow checks; restrict any approval to Vietnam |
| Runtime export changes pooling, tokenization, or precision | Fail the recipe and parity gates; retain the old release |
| New model changes cosine scale | Recalibrate only on development cases; freeze thresholds before sealed evaluation |
| CPU variability defeats p95 or startup targets | Require target-host measurement; revise hosting demand or retain Qwen without claiming the target passed |
| Better ranking hides invalid or misattributed evidence | Separate retrieval gains from known corpus defects; require corpus-owned validity and relation tests |
| Model, storage, and chunking changes confound results | Change one factor at a time against frozen controls |
| Retained releases exceed disk or the single runtime exceeds memory | Hosting and database designs must demonstrate measured release-storage and single-runtime rollback capacity before promotion |

This design has no measured Granite or fine-tune result on banhmi, no target-host latency result, no verified model artifact hashes, and no checked deployed state. Snapshot availability, actual input-length distributions, batch throughput, GPU entitlement, and exact storage cost remain unverified. Independent acceptance and hosted execution remain required. Linking this proposal from the documentation index belongs to the integrating owner because this task permits only this file.

## Owner questions

1. Is a 500 ms short-query p95 at 2 requests per second, bursts of 8, the right launch target? Recommend this target with a 2 vCPU constrained bake-off and concurrent PostgreSQL and application load. Start from `s2-general-4x8` (4 vCPU, 8 GiB), set the single runtime reservation from measurement, and grow only when measurements require it.
2. May the bake-off spend up to $200 on GitHub-hosted GPU/CPU work and artifacts? Recommend the ceiling with a 10,000-chunk pilot and a stop before the full run if the estimate exceeds it. This proposal authorizes no run.
3. Should a Vietnam-only quality winner be accepted when it fails another country's candidate gate? Recommend yes for Vietnam only, while the other five services keep Qwen unchanged. Do not claim a common ASEAN model until each country passes.
