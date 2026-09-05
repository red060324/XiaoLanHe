local metadata = redis.call('HMGET', KEYS[1], 'version', 'active', 'closed_at_ms')
if metadata[1] == false or metadata[1] ~= ARGV[1] then
  return {-1, 0}
end
if metadata[2] == '0' and metadata[3] ~= false then
  return {2, tonumber(metadata[3])}
end
if metadata[2] ~= '1' then
  return {-1, 0}
end
local redis_time = redis.call('TIME')
local now_ms = (tonumber(redis_time[1]) * 1000) + math.floor(tonumber(redis_time[2]) / 1000)
redis.call('HSET', KEYS[1], 'active', '0', 'closed_at_ms', tostring(now_ms))
return {1, now_ms}
