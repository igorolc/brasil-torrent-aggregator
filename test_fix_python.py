import requests
from lxml import html
import re

def test_extraction(query):
    print(f"--- Iniciando teste para: {query} ---")
    base_url = "https://redetorrent.com"
    search_url = f"{base_url}/index.php?s={query.replace(' ', '+')}"
    
    headers = {
        'User-Agent': 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36'
    }

    # 1. Busca na listagem
    print(f"Buscando em: {search_url}")
    response = requests.get(search_url, headers=headers)
    tree = html.fromstring(response.content)

    # LÓGICA RESILIENTE: Procuramos qualquer 'a' dentro de 'capa_lista'
    # Vamos listar todos os encontrados para ver qual é o correto
    items = tree.xpath("//div[@class='capa_lista']")
    found_any = False
    
    for item in items:
        link = item.xpath(".//a/@href")
        title_text = item.xpath(".//a/@title") or item.xpath(".//h2/text()") or item.xpath(".//a/text()")
        
        if link:
            url = link[0]
            desc = title_text[0].strip() if title_text else "Sem título"
            print(f"[*] Encontrado: {desc} -> {url}")
            
            # Se o título bater com a busca, usamos esse
            if query.lower() in desc.lower() or "todo tempo" in desc.lower():
                movie_url = url
                print(f"[!] MATCH ENCONTRADO: {desc}")
                found_any = True
                break

    if not found_any:
        print("[-] Nenhum match exato encontrado. Usando o primeiro da lista como fallback...")
        if items:
            movie_url = items[0].xpath(".//a/@href")[0]
        else:
            print("[-] Nenhum link encontrado na listagem.")
            return

    # 2. Acessando a página do filme
    print(f"Acessando detalhes: {movie_url}")
    response = requests.get(movie_url, headers=headers)
    tree = html.fromstring(response.content)

    # LÓGICA DE ROTEAMENTO (Onde o BeTor se perde):
    # Não vamos checar se existe 'capas_pequenas' para decidir se é um item.
    # Vamos focar em extrair os dados do conteúdo principal.
    
    # Extração de Título (Genérica como no nosso Go)
    title = tree.xpath("//h1/text()")
    if title:
        print(f"[+] Título extraído: {title[0].strip()}")
    else:
        print("[-] Título não encontrado com h1.")

    # Extração de Magnets (Com metadados estilo Go)
    magnet_elements = tree.xpath("//a[contains(@href, 'magnet')]")
    
    # Se não achar nada, tenta o padrão de botões
    if not magnet_elements:
        magnet_elements = tree.xpath("//a[contains(@class, 'newdawn') and contains(@href, 'magnet')]")

    if magnet_elements:
        print(f"[+] SUCESSO! Encontrados {len(magnet_elements)} links:")
        
        # Pega info global da página para fallbacks (estilo Go)
        global_info = " ".join(tree.xpath("//div[@id='informacoes']//p//text()")).upper()
        page_title = "".join(tree.xpath("//title/text()")).upper()
        
        for el in magnet_elements:
            href = el.xpath("./@href")[0]
            context_text = el.xpath("./@title") or el.xpath("./text()")
            context_text = context_text[0].strip() if context_text else ""
            
            # Regex de Qualidade/Resolução (Estilo Go)
            quality_regex = r'(720P|1080P|2160P|4K|ULTRA HD|HDR|DV|WEB-DL|BLURAY|CAM|TS|HD|R5|5\.1)'
            audio_regex = r'(DUAL|AUDIO|DUBLADO|LEGENDADO|PORTUGUES|INGLES|ENG|PT-BR)'
            
            q_matches = re.findall(quality_regex, context_text.upper())
            a_matches = re.findall(audio_regex, context_text.upper())
            
            # Fallback se o contexto do link for pobre (Estilo Go)
            if not q_matches:
                q_matches = re.findall(quality_regex, global_info + " " + page_title)
            if not a_matches:
                a_matches = re.findall(audio_regex, global_info + " " + page_title)
            
            # Limpeza e Formatação
            metadata = ".".join(sorted(list(set(q_matches + a_matches))))
            print(f"    - Link: {href[:50]}...")
            print(f"      Metadata: {metadata if metadata else 'N/A'}")
            print(f"      Contexto Original: {context_text}")
            print("-" * 20)
    else:
        print("[-] FALHA: Nenhum magnet link encontrado.")

if __name__ == "__main__":
    # Testando com o filme que o BeTor não encontra
    test_extraction("Todo Tempo Que Temos")
    print("\n")
    # Testando com um exemplo que funciona (The Boys) para comparar
    test_extraction("The Boys")
