# Atlas Cloud Native Streaming Platform

## Network Architecture Standard & Agent Development Contract (v1.0)

### 1. Architecture Authority

This document is the **authoritative source** for Atlas network architecture decisions.
Any Kubernetes manifest, Helm values, Terraform module, or automation script related to networking **MUST** comply with this document.

**⚠️ AI AGENT DIRECTIVE:**
When generated code, external documentation, or default component configurations conflict with this document, **this document takes absolute precedence.**
---

### 2. Decision Hierarchy

Network engineering decisions within the Atlas platform follow this strict priority order:

1. **Architecture Principles** (This document)
2. **Security & Zero-Trust Constraints** (Identity-based isolation)
3. **Operational Stability & Runtime Safety** (Predictability over peak benchmarks)
4. **Data Plane Performance** (Throughput > Latency variance)
5. **Platform Defaults** (Atlas baseline values)
6. **Component-specific Defaults** (e.g., Default Envoy/Cilium behaviors)

---

### 3. Architecture Decision Summary (ADR RAG Index)

**Atlas network architecture core decisions at a glance:**

- Gateway API is strictly for external traffic governance.
- Stateful topology lifecycle belongs exclusively to Domain Operators.
- Streaming data plane natively bypasses proxy layers.
- Cilium provides the programmable network dataplane.
- Security relies on cryptographic identity, not ephemeral IPs.
- IPv4-primary is the foundational platform baseline.

---

### 4. Core Architecture Principles

> **Principle 1: Gateway API 管入口，不管状态 (Gateway API for North-South Only)**
> Gateway API **MUST NOT** become the authority for Stateful Service topology. It handles HTTP Routing, TLS Termination, and External Access Policy.
> _Note:_ It is permissible for a Domain Operator to automatically generate `TCPRoute` or `TLSRoute` resources, but the Gateway Controller itself must not infer or manage the stateful topology.

> **Principle 2: 控制面抽象，数据面直通 (Abstract Control Plane, Direct Data Plane)**
> The Streaming Data Plane (e.g., Flink to Redpanda/Kafka) must communicate directly over the CNI. You **MUST NOT** introduce unnecessary network hops, sidecars, or Gateway APIs into the East-West streaming data path.
> _Exception:_ Management, observability, debugging, or security inspection traffic may use additional proxy or network layers if explicitly approved.

> **Principle 3: 身份优于连通性 (Identity > IP)**
> Workload security **MUST NOT** depend on ephemeral Pod IPs. Static network ranges (CIDRs) may only be used for defining external trust boundaries (e.g., Corporate Networks).

---

### 5. Stateful Service Networking Rules

For **ANY** Stateful workload within Atlas, the network identity lifecycle **MUST** be managed by the corresponding domain controller.

- **Streaming/Message Brokers (Kafka, Redpanda):**
- Broker ID mapping, `advertised.listeners`, and listener lifecycle are strictly Operator-managed.
- Stateful broker-to-client communication **SHOULD** prefer stable endpoint discovery mechanisms (e.g., Headless Services).

- **Databases (PostgreSQL, ClickHouse):**
- Primary/Replica identity and Read/Write endpoint ownership must be managed by the Database Operator.

- **Storage and Metadata Services (MinIO, Iceberg REST Catalog):**
- Storage node identity and metadata endpoint stability must bypass ephemeral proxy layers where possible.

---

### 6. Component Boundaries & API Decoupling

| Component              | Allowed Scope                                    | Hard Boundary (DO NOT CROSS)                                                                      |
| ---------------------- | ------------------------------------------------ | ------------------------------------------------------------------------------------------------- |
| **Domain Operator**    | Workload lifecycle, topology, internal endpoints | Must not bypass platform networking abstractions to directly manipulate infrastructure resources. |
| **Gateway Controller** | North-South traffic, TLS, external exposure      | Must not manage intra-cluster Service Discovery                                                   |
| **Cilium (CNI)**       | Pod connectivity, eBPF datapath, NetworkPolicy   | **Application logic MUST NOT depend on Cilium-specific APIs** (to prevent vendor lock-in).        |

---

### 7. Anti-Pattern Catalog

**⚠️ AI AGENT DIRECTIVE:** Review these anti-patterns before generating any manifests.

#### ❌ AP-001: Gateway as Data Bus

**BAD:** Flink `->` Gateway API / Ingress `->` Kafka Broker
**REASON:** Gateway is an edge proxy, not a high-throughput streaming data router. It breaks TCP congestion control and inflates tail latency.

#### ❌ AP-002: Mandatory Proxy Layer in Streaming Data Plane

**BAD:** Kafka Broker `->` Any Mandatory Proxy (Envoy Sidecar / Ambient Node Proxy / Gateway) `->` Network
**REASON:** Forcing the streaming data path through a proxy adds memory overhead, CPU context switching, and latency variance (jitter), which severely impacts streaming alignment and Checkpoint times.

#### ❌ AP-003: IP-Based Internal Security

**BAD:** `NetworkPolicy` allowing ingress from `10.244.1.0/24`.
**REASON:** Pod IPs are ephemeral. Internal network security must use `podSelector` and `namespaceSelector`.

---

### 8. Environment Specific Standards

#### 8.1 IPv4 / IPv6 Standards (The Dual-Stack Principle)

**IPv6 support must be intentional, not accidental.**

- **Production:** `IPv4 Primary` is recommended. If Dual Stack is required, it must be validated end-to-end. Partial Dual Stack is strictly forbidden.
- **Envoy DNS Safeguard:** Always set `dnsLookupFamily: V4Only` in Envoy Gateway configurations to prevent xDS failures caused by unroutable AAAA records.
- **Local Development:** Local Atlas Kubernetes deployments—particularly on macOS Apple Silicon runtimes utilizing OrbStack and Kind—**MUST default to IPv4 Only**. This mitigates cross-platform DNS resolution inconsistencies and prioritizes operational stability during local iteration.

---

### 9. Agent Pre-Generation Checklist

**⚠️ AI AGENT DIRECTIVE:** Before writing or modifying any Kubernetes YAML, Terraform, or network configuration, you MUST evaluate this reasoning chain:

1. **Traffic Direction:** Is this North-South (External) or East-West (Internal)?
2. **Workload Type:** Is the target Stateful (requires Operator/Headless) or Stateless (ClusterIP is fine)?
3. **Ownership Boundary:** Who logically owns this resource? (Gateway for exposure, Operator for topology, CNI for routing).
4. **Resource Generation:** _Can this resource be generated by a controller instead of manually declared?_ (Do not bypass Operators by manually hardcoding Services or Endpoints).
5. **Security Model:** Does the policy rely on Identity/Labels rather than IPs?
6. **Performance Impact:** Does this configuration add a proxy hop, NAT, or buffer to the streaming data plane? (If yes, reject).
