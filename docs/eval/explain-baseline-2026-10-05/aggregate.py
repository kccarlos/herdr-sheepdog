# Usage: python3 aggregate.py T1_DIR T2_DIR  (T1_DIR: an eval sim --out dir; T2_DIR: a bench run --out dir with runs/)
import json,glob,sys,collections
def m(xs):
  xs=[x for x in xs if x is not None]; return (sum(xs)/len(xs)) if xs else None
def f(x,p=2): return '–' if x is None else f'{x:.{p}f}'
def runs_t2():
  return [json.load(open(p)) for p in sorted(glob.glob(sys.argv[2] + '/runs/*.json'))]
def runs_t1():
  return json.load(open(sys.argv[1] + '/report.json'))['runs']
for name,runs in (('tier1',runs_t1()),('tier2',runs_t2())):
  print('###',name)
  by=collections.defaultdict(list)
  for r in runs: by[r['scenario']].append(r)
  print('| scenario | pass | orch cov | voice cov | voice of orch | orch comp | voice comp | orch words | voice words | depth orch | depth voice | pad orch | pad voice | not consulted (follow-ups) |')
  for sid,rs in by.items():
    E=[r['explain'] for r in rs if r.get('explain')]
    O=[e['orchestrator'] for e in E]; V=[e.get('voice') for e in E if e.get('voice')]
    def words(sides,simple=None):
      w=[]
      for e,s in zip(E,sides):
        for ex,n in zip(e['exchanges'],s['words']):
          if n>=0: w.append(n)
      return m(w)
    def ratio(sides,a,b): 
      A=sum(s[a] for s in sides); B=sum(s[b] for s in sides); return f'{A}/{B}' if B else '–'
    nc=sum(e['not_consulted'] for e in E); fnc=sum(e['follow_ups_not_consulted'] for e in E)
    fu=sum(1 for e in E for x in e['exchanges'] if x['kind'] in('then','more','why','example'))
    print(f"| {sid} | {sum(r['status']=='pass' for r in rs)}/{len(rs)} | {f(m([o.get('coverage') for o in O]))} | {f(m([v.get('coverage') for v in V]))} | {f(m([v.get('of_orchestrator') for v in V]))} | {f(m([o.get('comprehension') for o in O]))} | {f(m([v.get('comprehension') for v in V]))} | {f(words(O),0)} | {f(words(V),0) if V else '–'} | {ratio(O,'depth_passed','depth_asked')} | {ratio(V,'depth_passed','depth_asked') if V else '–'} | {ratio(O,'padding_passed','padding_asked')} | {ratio(V,'padding_passed','padding_asked') if V else '–'} | {nc} ({fnc}/{fu}) |")
  # failure labels
  c=collections.Counter()
  for r in runs:
    for x in r.get('failures',[]):
      d=x['detail'].split(':')[0]; c[(x['type'], d.split(' (')[0] if not d.startswith('explain') else d.split('_t')[0]+(' (voice)' if '(voice)' in d else ' (orch)'))]+=1
  print(c.most_common())
