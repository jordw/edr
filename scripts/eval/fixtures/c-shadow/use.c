int compute(int x);

int run(void) {
    int result = compute(5);
    int compute = 42;  /* local shadows global; must remain */
    return result + compute;
}

int main(void) { return run(); }
