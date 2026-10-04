# Retained size methodology

## Goal

For an allocation site **S**, report the shallow bytes that would become
unreachable if **all** objects of site S were deleted while every other site
and every GC root remained. This is the site's *exclusive* retained size.

## Algorithm

1. Add a synthetic **virtual root** `R` with strong edges to every GC root.
2. Compute immediate dominators with **Lengauer-Tarjan** on the reachability
   graph (`retain/dominators.go`). The dominator `idom(v)` is the unique node
   through which every root-to-`v` path must pass.
3. Attribute each reachable node `v` to its *group dominator*:
   - walk the idom chain to the nearest ancestor whose site differs from
     `site(v)`;
   - if that ancestor is a real node with a site, the node's bytes are credited
     to that site;
   - if the chain reaches the virtual root, `v` is directly rooted and credited
     to its own site (a directly-rooted site owns itself);
   - nodes with empty site that are only reachable via the virtual root go to
     `Unattributed`;
   - nodes unreachable from every root in the strong graph go to
     `Unreachable` (never attributed to a site).

Every reachable node has exactly one owner, so the sum over
`BySite[*].RetainedSize + Unattributed + Unreachable` equals `TotalShallow`
(asserted in the tests).

## Why sharing is counted once

An object with two independent retainers is dominated only by the node that
*gates all* paths to it. If `v` is reachable through two different sites, the
dominator is whichever single site (or root) all paths pass through; the other
site gets none of `v`. It can therefore never be counted for both sites.

Diamond example (`root(A) -> {1(B),2(B)} -> 3(C)`): node 3 is dominated by the
A root, not by either B; deleting one B leaves 3 reachable through the other.

## Why cycles terminate and don't double count

Dominators are defined for cyclic graphs and the dominator **tree** is always
acyclic, so the ownership walk and subtree accounting cannot loop. Members of
an SCC are attributed via their nearest out-of-group dominator exactly once.
Cycle tests (`retain.TestCycleNoInfiniteNoDoubleCount`) assert termination and
that the total is conserved.

## Weak references

Weak edges are recorded for inspection but excluded from the graph passed to
the dominator pass: a weakly held object is not retained by its weak referrer
and may be unreachable in the strong graph (reported as such).

## Bounded memory

For large snapshots the graph is a paged external CSR:

- forward and reverse adjacency are produced by external sort of spilled edge
  runs;
- node size/root/site arrays and every Lengauer-Tarjan working array
  (`semi,parent,ancestor,idom,label,size,child`) are LRU-paged int64 arrays;
- LT bucket lists spill to per-key files (append once, drain once).

Thus peak memory is set by the configured budget, not by snapshot size.
