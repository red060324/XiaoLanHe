local metadata = redis.call('HMGET', KEYS[1], 'version', 'active', 'starts_at_ms', 'ends_at_ms')
if metadata[1] == false or metadata[1] ~= ARGV[2] then
  return {-5, '', 0}
end
local starts_at_ms = tonumber(metadata[3])
local ends_at_ms = tonumber(metadata[4])
if starts_at_ms == nil or ends_at_ms == nil then
  return {-5, '', 0}
end
local redis_time = redis.call('TIME')
local now_ms = (tonumber(redis_time[1]) * 1000) + math.floor(tonumber(redis_time[2]) / 1000)

local buyer = redis.call('HGET', KEYS[3], ARGV[3])
local expected_buyer = ARGV[1] .. '|' .. ARGV[4]
if buyer then
  if buyer ~= expected_buyer then
    return {-4, '', now_ms}
  end
  local request = redis.call('HGET', KEYS[4], ARGV[1])
  if not request then
    return {-5, '', now_ms}
  end
  local queued_pattern = '^' .. ARGV[3] .. '|' .. ARGV[4] .. '|queued|([0-9]+)$'
  local released_pattern = '^' .. ARGV[3] .. '|' .. ARGV[4] .. '|released|([0-9]+)|[^|]+$'
  local reserved_at_ms = tonumber(string.match(request, queued_pattern) or string.match(request, released_pattern))
  if reserved_at_ms == nil then
    return {-5, '', now_ms}
  end
  return {2, ARGV[1], reserved_at_ms}
end

if now_ms < starts_at_ms then
  return {-1, '', now_ms}
end
if metadata[2] ~= '1' or now_ms >= ends_at_ms then
  return {-2, '', now_ms}
end
local reserved_at_ms = tonumber(ARGV[5])
local max_age_ms = tonumber(ARGV[6])
if reserved_at_ms == nil or max_age_ms == nil or reserved_at_ms > now_ms or now_ms - reserved_at_ms > max_age_ms then
  return {-5, '', now_ms}
end
if reserved_at_ms < starts_at_ms or reserved_at_ms >= ends_at_ms then
  return {-5, '', now_ms}
end

local stock = tonumber(redis.call('GET', KEYS[2]))
if stock == nil then
  return {-5, '', now_ms}
end
if stock <= 0 then
  return {-3, '', now_ms}
end

local ttl_ms = redis.call('PTTL', KEYS[1])
if ttl_ms <= 0 then
  return {-5, '', now_ms}
end
redis.call('DECR', KEYS[2])
redis.call('HSET', KEYS[3], ARGV[3], expected_buyer)
redis.call('HSET', KEYS[4], ARGV[1], ARGV[3] .. '|' .. ARGV[4] .. '|queued|' .. tostring(reserved_at_ms))
redis.call('ZADD', KEYS[5], reserved_at_ms, ARGV[1])
for index = 1, 5 do
  redis.call('PEXPIRE', KEYS[index], ttl_ms)
end
return {1, ARGV[1], reserved_at_ms}
