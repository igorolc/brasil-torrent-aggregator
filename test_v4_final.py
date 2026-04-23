import requests
import json
import sys

def test_all_indexers(query):
    indexers = [
        "rede_torrent",
        "comando",
        "bludv",
        "vaca_torrent",
        "starck_filmes",
        "torrent_dos_filmes"
    ]
    
    results_summary = {}
    
    print(f"\n=== PESQUISANDO POR: {query} ===")
    
    for idx in indexers:
        url = f"http://localhost:8080/indexers/{idx}?q={query}"
        try:
            response = requests.get(url, timeout=30)
            if response.status_code != 200:
                continue
                
            data = response.json()
            results = data.get('results', [])
            
            print(f"[{idx}] Encontrados {len(results)} resultados.")
            for i, res in enumerate(results[:5]):
                print(f"  ({i+1}) Title: {res.get('title')}")
                print(f"      Original: {res.get('original_title')}")
            
            results_summary[f"{idx}_{query}"] = results
                
        except Exception as e:
            print(f"[{idx}] ERRO: {e}")

    return results_summary

if __name__ == "__main__":
    r1 = test_all_indexers("We Live In Time")
    r2 = test_all_indexers("Deadpool")
    
    all_results = {**r1, **r2}
    with open('results_blindagem_final.json', 'w', encoding='utf-8') as f:
        json.dump(all_results, f, ensure_ascii=False, indent=4)
    print("\nArquivo 'results_blindagem_final.json' gerado.")
