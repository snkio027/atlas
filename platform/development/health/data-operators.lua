-- Readiness of desired CRs, never authorization to mutate their children.
local s = obj.status or {}
local generation = (obj.metadata or {}).generation
if generation == nil then
  return {status = "Progressing", message = "Missing generation"}
end
if obj.kind == "FlinkDeployment" then
  if s.observedGeneration ~= generation then
    return {status = "Progressing", message = "Waiting for current Flink generation"}
  end
  if s.lifecycleState == "FAILED" or s.lifecycleState == "ROLLED_BACK" then
    return {status = "Degraded", message = s.error or s.lifecycleState}
  end
  if s.lifecycleState == "STABLE" and s.jobManagerDeploymentStatus == "READY" then
    return {status = "Healthy", message = "Current Flink session is stable"}
  end
  return {status = "Progressing", message = "Waiting for stable Flink session"}
end
-- CNPG 1.30.1 does not populate observedGeneration on Ready. Match the
-- readiness fields this release actually publishes; this is not proof that
-- every spec field has rolled out. Runtime verification must inspect children.
if obj.apiVersion == "postgresql.cnpg.io/v1" and obj.kind == "Cluster" then
  local ready = nil
  for _, c in ipairs(s.conditions or {}) do
    if c.type == "Ready" then
      if ready ~= nil then
        return {status = "Progressing", message = "Ambiguous CNPG Ready conditions"}
      end
      ready = c
    end
  end
  if ready == nil then
    return {status = "Progressing", message = "Waiting for CNPG Ready condition"}
  end
  if ready.observedGeneration ~= nil and ready.observedGeneration ~= 0 and
    ready.observedGeneration ~= generation then
    return {status = "Progressing", message = "Stale CNPG Ready condition"}
  end
  if ready.status == "False" then
    -- CNPG sets ClusterIsNotReady for every non-healthy phase, including
    -- normal first-primary setup. Only these known startup phases may wait;
    -- a negative condition is never sufficient evidence of readiness.
    if ready.reason == "ClusterIsNotReady" and
       (s.phase == "Setting up primary" or s.phase == "Waiting for the instances to become active") then
      return {status = "Progressing", message = s.phase}
    end
    return {status = "Degraded", message = ready.message or ready.reason or "CNPG not ready"}
  end
  local spec = obj.spec or {}
  if ready.status == "True" and s.phase == "Cluster in healthy state" and
    type(spec.instances) == "number" and spec.instances > 0 and
    s.instances == spec.instances and s.readyInstances == spec.instances and
    spec.imageName ~= nil and s.image == spec.imageName and
    s.currentPrimary ~= nil and s.currentPrimary ~= "" and s.currentPrimary == s.targetPrimary then
    return {status = "Healthy", message = "CNPG ready; child rollout requires runtime verification"}
  end
  return {status = "Progressing", message = "Waiting for CNPG instances, image and primary readiness"}
end
-- Strimzi records generation on status.
if obj.apiVersion == "kafka.strimzi.io/v1" and s.observedGeneration ~= generation then
  return {status = "Progressing", message = "Waiting for current Kafka generation"}
end
local ready = false
for _, c in ipairs(s.conditions or {}) do
  local current = c.observedGeneration == generation or
    (obj.apiVersion == "kafka.strimzi.io/v1" and s.observedGeneration == generation)
  if current and ((c.type == "NotReady" and c.status == "True") or
    (c.type == "Ready" and c.status == "False")) then
    return {status = "Degraded", message = c.message or c.reason or "Not ready"}
  end
  if current and c.type == "Ready" and c.status == "True" then ready = true end
end
if ready then return {status = "Healthy", message = "Current operator condition is ready"} end
return {status = "Progressing", message = "Waiting for current Ready condition"}
