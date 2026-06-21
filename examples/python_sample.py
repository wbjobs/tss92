import time

def profile_target():
    data = []
    for i in range(10000):
        data.append(i * i)
    total = sum(data)
    time.sleep(0.1)
    return total

if __name__ == "__main__":
    result = profile_target()
    print(f"Result: {result}")
