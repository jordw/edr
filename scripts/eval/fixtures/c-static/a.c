#include <stdio.h>

static int helper(void) { return 1; }

int use_a(void) { return helper(); }

int main(void) { printf("%d\n", use_a()); return 0; }
