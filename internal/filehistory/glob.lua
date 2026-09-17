-- Match the original path.Match grammar segment by segment. ** is recursive
-- only as a complete segment. Both levels use iterative dynamic programming;
-- repeated wildcards cannot create recursive/exponential backtracking.
local function history_runes(value)
 local out={}
 local i=1
 while i<=#value do
  local first=string.byte(value,i)
  local width=first<128 and 1 or (first>=240 and 4 or (first>=224 and 3 or (first>=192 and 2 or 1)))
  local code=width==1 and first or first%(2^(7-width))
  local valid=true
  for j=i+1,i+width-1 do
   local b=string.byte(value,j)
   if not b or b<128 or b>=192 then valid=false; break end
   code=code*64+b-128
  end
  if not valid then code=65533; width=1 end
  table.insert(out,code)
  i=i+width
 end
 return out
end

local function history_trim(value)
 local runes=history_runes(value)
 local function space(c)
  return c==32 or (c>=9 and c<=13) or c==133 or c==160 or c==5760
   or (c>=8192 and c<=8202) or c==8232 or c==8233 or c==8239 or c==8287 or c==12288
 end
 -- Find the byte boundaries corresponding to TrimSpace, then trim slashes,
 -- exactly as the original matchesAnyVersioningGlob helper does.
 local first,last=1,#runes
 while first<=last and space(runes[first]) do first=first+1 end
 while last>=first and space(runes[last]) do last=last-1 end
 local offsets={}
 local offset=1
 for j,c in ipairs(runes) do
  offsets[j]=offset
  offset=offset+(c<128 and 1 or (c<2048 and 2 or (c<65536 and 3 or 4)))
 end
 offsets[#runes+1]=#value+1
 value=first>last and '' or string.sub(value,offsets[first],offsets[last+1]-1)
 return string.gsub(string.gsub(value,'^/+',''),'/+$','')
end

local function history_segment(pattern,value)
 local letters=history_runes(pattern)
 local runes=history_runes(value)
 local positions={[1]=true}
 local i=1
 while i<=#letters do
  local c=letters[i]
  local nextPositions={}
  if c==42 then
   local active=false
   for j=1,#runes+1 do active=active or positions[j]; if active then nextPositions[j]=true end end
   i=i+1
  elseif c==91 then
   i=i+1
   local negate=letters[i]==94
   if negate then i=i+1 end
   local ranges={}
   local function letter()
    if letters[i]==92 then i=i+1 end
    local result=letters[i]
    i=i+1
    return result
   end
   while i<=#letters and letters[i]~=93 do
    local low=letter()
    local high=low
    if letters[i]==45 then i=i+1; high=letter() end
    if not low or not high then return false end
    table.insert(ranges,{low,high})
   end
   if letters[i]~=93 or #ranges==0 then return false end
   i=i+1
   for j in pairs(positions) do
    local actual=runes[j]
    if actual then
     local found=false
     for _,range in ipairs(ranges) do if actual>=range[1] and actual<=range[2] then found=true; break end end
     if found~=negate then nextPositions[j+1]=true end
    end
   end
  else
   local any=c==63
   if c==92 then i=i+1; c=letters[i]; any=false end
   if not c then return false end
   for j in pairs(positions) do
    if runes[j] and (any or runes[j]==c) then nextPositions[j+1]=true end
   end
   i=i+1
  end
  positions=nextPositions
 end
 return positions[#runes+1] or false
end

local function history_glob(pattern,value)
 pattern=history_trim(pattern)
 value=history_trim(value)
 if pattern=='' or value=='' then return false end
 local function split(s)
  local out={}
  for segment in string.gmatch(s..'/','(.-)/') do table.insert(out,segment) end
  return out
 end
 local parts,segments=split(value),split(pattern)
 local positions={[1]=true}
 for _,segment in ipairs(segments) do
  local nextPositions={}
  if segment=='**' then
   local active=false
   for j=1,#parts+1 do active=active or positions[j]; if active then nextPositions[j]=true end end
  else
   for j in pairs(positions) do
    if parts[j] and history_segment(segment,parts[j]) then nextPositions[j+1]=true end
   end
  end
  positions=nextPositions
 end
 return positions[#parts+1] or false
end
