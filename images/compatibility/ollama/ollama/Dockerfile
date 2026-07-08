FROM ollama/ollama:0.31.1

# Bake a tiny model into the image.
RUN bash -c '                                  \
    ( ollama serve ) & serverpid="$!";         \
    sleep 5;                                   \
    ollama pull smollm:135m &&                 \
    kill "$serverpid"       &&                 \
    wait "$serverpid"                          \
'
