local metadata = redis.call('HMGET', KEYS[1], 'version', 'ends_at_ms')
if metadata[1] == false or metadata[1] ~= ARGV[1] then
  return -1
end
local redis_time = redis.call('TIME')
local now_ms = (tonumber(redis_time[1]) * 1000) + math.floor(tonumber(redis_time[2]) / 1000)
if tonumber(metadata[2]) == nil or now_ms >= tonumber(metadata[2]) then
  return -2
end
redis.call('HSET', KEYS[1], 'active', '1')
return 1
