import requests
import json
import sys

def test_search():
    query = "We Live In Time"
    url = f"http://localhost:8080/search/redetorrent?query={query}"
    
    print(f"Pesquisando: {query}...")
    try:
        response = requests.get(url, timeout=30)
        data = response.json()
        
        # Salva em UTF-8 real
        with open('results_v4.json', 'w', encoding='utf-8') as f:
            json.dump(data, f, ensure_ascii=False, indent=4)
        
        print(f"Sucesso! Encontrados {len(data.get('results', []))} resultados.")
        for res in data.get('results', []):
            print(f"- Título: {res.get('title')}")
            print(f"  Original: {res.get('original_title')}")
            print(f"  Link: {res.get('guid')[:60]}...")
            
    except Exception as e:
        print(f"Erro: {e}")

if __name__ == "__main__":
    test_search()
