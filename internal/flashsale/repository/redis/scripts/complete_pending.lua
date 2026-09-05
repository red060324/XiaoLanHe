local request = redis.call('HGET', KEYS[1], ARGV[1])
local expected = ARGV[2] .. '|' .. ARGV[3] .. '|queued|' .. ARGV[4]
local released_prefix = ARGV[2] .. '|' .. ARGV[3] .. '|released|' .. ARGV[4] .. '|'
if request == expected or (request and string.sub(request, 1, string.len(released_prefix)) == released_prefix) then
  redis.call('ZREM', KEYS[2], ARGV[1])
  return 1
end
if not request then
  return 2
end
return -1
