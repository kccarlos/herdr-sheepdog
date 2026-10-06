# usage: . env.sh  (inside a subshell) — loads .env, drops every Google/OpenAI key
set -a; . /Users/kc/Documents/herdr-sheepdog/.env; set +a
unset VERTEX_API_KEY VERTEX_PROJECT OPENAI_API_KEY GITHUB_PAT OPENROUTER_API_KEY
