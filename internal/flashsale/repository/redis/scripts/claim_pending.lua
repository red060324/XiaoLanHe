local version = redis.call('HGET', KEYS[1], 'version')
if not version then
  return {-1}
end

local stale_ms = tonumber(ARGV[1])
local lease_ms = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])
if stale_ms == nil or lease_ms == nil or limit == nil or stale_ms <= 0 or lease_ms <= 0 or limit < 1 then
  return {-1}
end

local redis_time = redis.call('TIME')
local now_ms = (tonumber(redis_time[1]) * 1000) + math.floor(tonumber(redis_time[2]) / 1000)
local cutoff_ms = now_ms - stale_ms
local next_score = now_ms + lease_ms - stale_ms
local candidates = redis.call('ZRANGEBYSCORE', KEYS[3], '-inf', cutoff_ms, 'LIMIT', 0, limit)
local result = {version}

for _, request_id in ipairs(candidates) do
  local marker = redis.call('HGET', KEYS[2], request_id)
  if marker then
    local user_id, digest, reserved_at_ms = string.match(marker, '^([0-9]+)|([a-f0-9]+)|queued|([0-9]+)$')
    if user_id and digest and reserved_at_ms then
      redis.call('ZADD', KEYS[3], next_score, request_id)
      table.insert(result, request_id)
      table.insert(result, user_id)
      table.insert(result, digest)
      table.insert(result, reserved_at_ms)
    else
      redis.call('ZREM', KEYS[3], request_id)
    end
  else
    redis.call('ZREM', KEYS[3], request_id)
  end
end
return result
