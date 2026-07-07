-- atomic token bucket. redis runs the whole script as one unit, so the
-- read-compute-write race in the naive store can't happen here.
-- KEYS[1] = bucket key
-- ARGV = limit, window (microseconds), burst
-- returns {allowed, remaining, retry_after_us, reset_at_us}

-- redis TIME is the single clock; node clocks never enter the math.
-- calling TIME before writes is fine on redis >= 5 (effect replication).
-- all time math in integer microseconds: fits a double exactly, and
-- avoids lua's %.14g tostring mangling large timestamps
local time = redis.call('TIME')
local now = time[1] * 1000000 + time[2]

local limit = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local burst = tonumber(ARGV[3])
local rate = limit / window -- tokens per microsecond

local state = redis.call('HMGET', KEYS[1], 'tokens', 'last')
local tokens = burst
if state[1] then
  tokens = tonumber(state[1])
  local elapsed = now - tonumber(state[2])
  if elapsed > 0 then
    tokens = math.min(burst, tokens + elapsed * rate)
  end
end

local allowed = 0
local retry_after = 0
if tokens >= 1 then
  tokens = tokens - 1
  allowed = 1
else
  retry_after = math.ceil((1 - tokens) / rate)
end
local reset_at = now + math.ceil((burst - tokens) / rate)

-- format explicitly: letting the bridge stringify numbers goes through
-- %.14g, which corrupts microsecond timestamps
redis.call('HSET', KEYS[1],
  'tokens', string.format('%.17g', tokens),
  'last', string.format('%d', now))
-- state is reconstructible once the bucket refills, so let idle keys die
redis.call('PEXPIRE', KEYS[1], math.ceil(burst / rate / 1000))

return {allowed, math.floor(tokens), retry_after, reset_at}
