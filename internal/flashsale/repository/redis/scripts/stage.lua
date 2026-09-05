local current_version = redis.call('HGET', KEYS[1], 'version')
local requested_version = ARGV[1]
local starts_at_ms = ARGV[2]
local ends_at_ms = ARGV[3]
local total_stock = ARGV[4]
local remaining_stock = ARGV[5]
local expires_at_ms = tonumber(ARGV[6])

local redis_time = redis.call('TIME')
local now_ms = (tonumber(redis_time[1]) * 1000) + math.floor(tonumber(redis_time[2]) / 1000)
if expires_at_ms == nil or expires_at_ms <= now_ms then
  return -2
end

if current_version then
  local current = redis.call('HMGET', KEYS[1], 'starts_at_ms', 'ends_at_ms', 'total_stock', 'active', 'closed_at_ms')
  if current_version ~= requested_version or current[1] ~= starts_at_ms or current[2] ~= ends_at_ms or current[3] ~= total_stock then
    if current[4] ~= '0' or current[5] ~= false or redis.call('EXISTS', KEYS[3], KEYS[4], KEYS[5]) ~= 0 then
      return -1
    end
    redis.call('HSET', KEYS[1],
      'version', requested_version,
      'starts_at_ms', starts_at_ms,
      'ends_at_ms', ends_at_ms,
      'total_stock', total_stock)
    redis.call('SET', KEYS[2], remaining_stock)
  end
  local stock = tonumber(redis.call('GET', KEYS[2]))
  if stock == nil or stock < 0 or stock > tonumber(total_stock) then
    return -1
  end
  redis.call('PEXPIREAT', KEYS[1], expires_at_ms)
  redis.call('PEXPIREAT', KEYS[2], expires_at_ms)
  return 2
end

if redis.call('EXISTS', KEYS[2], KEYS[3], KEYS[4], KEYS[5]) ~= 0 then
  return -1
end
redis.call('HSET', KEYS[1],
  'version', requested_version,
  'starts_at_ms', starts_at_ms,
  'ends_at_ms', ends_at_ms,
  'total_stock', total_stock,
  'active', '0')
redis.call('SET', KEYS[2], remaining_stock)
redis.call('PEXPIREAT', KEYS[1], expires_at_ms)
redis.call('PEXPIREAT', KEYS[2], expires_at_ms)
return 1
