-- atomic sliding window log over a sorted set, score = timestamp.
-- KEYS[1] = window key
-- ARGV = limit, window (microseconds), nonce
-- returns {allowed, remaining, retry_after_us, reset_at_us}

-- redis TIME as the single clock, integer microseconds throughout
-- (see token_bucket.lua for why)
local time = redis.call('TIME')
local now = time[1] * 1000000 + time[2]

local limit = tonumber(ARGV[1])
local window = tonumber(ARGV[2])

-- inclusive cutoff matches the in-memory prune: entries a full window
-- old are gone, one microsecond younger still counts
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now - window)

local count = redis.call('ZCARD', KEYS[1])
local allowed = 0
local retry_after = 0
local reset_at = now + window

if count < limit then
  allowed = 1
  count = count + 1
  -- nonce keeps two requests in the same microsecond from collapsing
  -- into one member and undercounting
  redis.call('ZADD', KEYS[1],
    string.format('%d', now),
    string.format('%d-%s', now, ARGV[3]))
else
  local oldest = redis.call('ZRANGE', KEYS[1], 0, 0, 'WITHSCORES')
  retry_after = tonumber(oldest[2]) + window - now
  local newest = redis.call('ZRANGE', KEYS[1], -1, -1, 'WITHSCORES')
  reset_at = tonumber(newest[2]) + window
end

-- every entry ages out within one window, so the key can too
redis.call('PEXPIRE', KEYS[1], math.ceil(window / 1000))

return {allowed, math.max(0, limit - count), retry_after, reset_at}
