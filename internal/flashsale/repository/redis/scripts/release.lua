local buyer = redis.call('HGET', KEYS[3], ARGV[2])
local expected_buyer = ARGV[1] .. '|' .. ARGV[3]
local request = redis.call('HGET', KEYS[4], ARGV[1])
local expected_request = ARGV[2] .. '|' .. ARGV[3] .. '|queued|' .. ARGV[4]
local released_request = ARGV[2] .. '|' .. ARGV[3] .. '|released|' .. ARGV[4] .. '|' .. ARGV[5]
if request == released_request then
  return 2
end
if not buyer and not request then
  return 2
end
if buyer == expected_buyer and request then
  -- The same request ID and digest may be admitted again after a technical
  -- rollback. A release from an older timestamp is complete but must not
  -- release the newer incarnation.
  local queued_pattern = '^' .. ARGV[2] .. '|' .. ARGV[3] .. '|queued|([0-9]+)$'
  local queued_at_ms = tonumber(string.match(request, queued_pattern))
  local released_at_ms = tonumber(ARGV[4])
  if queued_at_ms and released_at_ms and queued_at_ms > released_at_ms then
    return 2
  end
end
if buyer ~= expected_buyer or request ~= expected_request then
  return -1
end
local stock = tonumber(redis.call('GET', KEYS[2]))
local total_stock = tonumber(redis.call('HGET', KEYS[1], 'total_stock'))
if stock == nil or total_stock == nil or stock >= total_stock then
  return -1
end
redis.call('INCR', KEYS[2])
redis.call('ZREM', KEYS[5], ARGV[1])
redis.call('HSET', KEYS[4], ARGV[1], released_request)
if ARGV[6] == '1' then
  redis.call('HDEL', KEYS[3], ARGV[2])
end
return 1
